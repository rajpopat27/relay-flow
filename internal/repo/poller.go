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
//
// Run applies ReplaceRepos to the LIVE loop: it diffs the desired repo set
// against the running pollers on every wake, starting pollers for newly
// registered repos and canceling pollers for removed ones, so repos
// registered after serve starts are polled within one poll tick.
type PollerGroup struct {
	mu      sync.RWMutex
	pollers []*RepoPoller
	max     int
	handle  BatchHandler
	sem     chan struct{}

	// changed is signalled (non-blocking send) by ReplaceRepos so Run
	// re-diffs immediately instead of waiting for the next tick.
	changed chan struct{}

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
		max:     maxConcurrent,
		handle:  handle,
		sem:     make(chan struct{}, maxConcurrent),
		changed: make(chan struct{}, 1),
	}
}

// ReplaceRepos swaps the repo set polled by the group. The new set applies
// from the next scheduling cycle, or immediately if Run is live.
func (g *PollerGroup) ReplaceRepos(repos []*Repo) {
	g.mu.Lock()
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
	g.mu.Unlock()
	select {
	case g.changed <- struct{}{}:
	default:
	}
}

func (g *PollerGroup) snapshot() []*RepoPoller {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]*RepoPoller(nil), g.pollers...)
}

// reconcile diffs the desired poller set against running pollers keyed by
// repo: starts pollers for new repos, cancels pollers for removed repos,
// and leaves already-running pollers for still-present repos untouched
// (so their interval timers are not reset by a no-op re-register).
func (g *PollerGroup) reconcile(ctx context.Context, running map[*Repo]context.CancelFunc, wg *sync.WaitGroup) {
	want := make(map[*Repo]*RepoPoller, len(g.snapshot()))
	for _, p := range g.snapshot() {
		want[p.Repo] = p
	}
	for rp, cancel := range running {
		if _, ok := want[rp]; !ok {
			cancel()
			delete(running, rp)
		}
	}
	for rp, p := range want {
		if _, ok := running[rp]; ok {
			continue
		}
		pctx, cancel := context.WithCancel(ctx)
		running[rp] = cancel
		wg.Add(1)
		go func(p *RepoPoller) {
			defer wg.Done()
			p.Run(pctx)
		}(p)
	}
}

// Run starts one RepoPoller goroutine per repo in the current set, keeps
// the running set in sync with ReplaceRepos, and blocks until ctx is
// canceled. On each wake (poll tick or ReplaceRepos signal) it reconciles
// the running pollers against the desired set.
func (g *PollerGroup) Run(ctx context.Context) {
	interval := g.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	var wg sync.WaitGroup
	running := map[*Repo]context.CancelFunc{}
	stopAll := func() {
		for _, cancel := range running {
			cancel()
		}
	}
	g.reconcile(ctx, running, &wg)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopAll()
			wg.Wait()
			return
		case <-g.changed:
			g.reconcile(ctx, running, &wg)
		case <-ticker.C:
			g.reconcile(ctx, running, &wg)
		}
	}
}
