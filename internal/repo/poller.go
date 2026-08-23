package repo

import (
	"context"
	"sync"
	"time"

	"github.com/rajpopat27/relay-flow/internal/task"
)

// BatchHandler receives one polled parent batch for a repo. Routing,
// claiming, and run starts are the handler's job; pollers only fetch.
type BatchHandler func(context.Context, *Repo, []task.Ticket)

// RepoPoller polls one repo's task system on a fixed interval and hands
// each fetched parent batch to the handler. It never routes, claims, or
// compiles filters.
type RepoPoller struct {
	Repo     *Repo
	Interval time.Duration
	Handle   BatchHandler

	sem chan struct{} // shared concurrency cap; nil means uncapped
}

// Run polls immediately, then on every interval, until ctx is canceled.
func (p *RepoPoller) Run(ctx context.Context) {
	p.poll(ctx)
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *RepoPoller) poll(ctx context.Context) {
	if p.sem != nil {
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return
		}
	}
	batch, err := p.Repo.TaskSystem.Poll(ctx)
	if err != nil {
		return
	}
	p.Handle(ctx, p.Repo, batch)
}

// PollerGroup runs one lightweight timer goroutine (RepoPoller) per
// registered repo. A shared semaphore limits concurrent task-system polls
// to maxConcurrent (10 per design). Every poller uses the machine-wide
// poll interval supplied by the startup wiring.
type PollerGroup struct {
	mu      sync.RWMutex
	pollers []*RepoPoller
	max     int
	handle  BatchHandler
	sem     chan struct{}

	// Interval is the machine-wide poll interval (Machine.PollIntervalSeconds)
	// applied to every repo poller. Set by startup wiring before ReplaceRepos.
	Interval time.Duration
}

// NewPollerGroup builds a group capping concurrent polls at maxConcurrent.
func NewPollerGroup(maxConcurrent int, handle BatchHandler) *PollerGroup {
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}
	return &PollerGroup{
		max:    maxConcurrent,
		handle: handle,
		sem:    make(chan struct{}, maxConcurrent),
	}
}

// ReplaceRepos swaps the repo set polled by the group. The new set applies
// from the next scheduling cycle.
func (g *PollerGroup) ReplaceRepos(repos []*Repo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	interval := g.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	pollers := make([]*RepoPoller, 0, len(repos))
	for _, rp := range repos {
		pollers = append(pollers, &RepoPoller{
			Repo:     rp,
			Interval: interval,
			Handle:   g.handle,
			sem:      g.sem,
		})
	}
	g.pollers = pollers
}

func (g *PollerGroup) snapshot() []*RepoPoller {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]*RepoPoller(nil), g.pollers...)
}

// Run starts one RepoPoller goroutine per repo in the current set and
// blocks until ctx is canceled.
func (g *PollerGroup) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range g.snapshot() {
		wg.Add(1)
		go func(p *RepoPoller) {
			defer wg.Done()
			p.Run(ctx)
		}(p)
	}
	<-ctx.Done()
	wg.Wait()
}
