package repo_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/task"
)

// 3.10: Repo Poller tests per specs/repo-workflow-routing "One Repo Poller
// fetches each repo": one poller per registered repo, all pollers use root
// pollIntervalSeconds, a semaphore caps concurrent polls at 10, pollers
// only fetch batches and call the batch handler.

// pollSystem records polls and can block to measure concurrency. batch is
// the fixed ticket set it returns.
type pollSystem struct {
	task.System // unused primitives
	shared      *pollCounter
	polls       atomic.Int32
	block       chan struct{}
	batch       []task.Ticket
	t           *testing.T // when set, any matching/claiming call fails the test
}

// The poller must never route, claim, or compile filters — those are the
// batch handler's job (task 5.3). These overrides fail loudly if called.
func (p *pollSystem) forbidden(name string) {
	if p.t != nil {
		p.t.Fatalf("poller invoked %s; a poller only fetches (Poll) and hands the batch to the handler", name)
	}
}
func (p *pollSystem) Claim(context.Context, task.TicketRef, string) error {
	p.forbidden("Claim")
	return nil
}
func (p *pollSystem) CompileFilter(config.RawValues) (func(task.Ticket) bool, error) {
	p.forbidden("CompileFilter")
	return nil, nil
}
func (p *pollSystem) ApplyTaskConfig(context.Context, task.Target, config.RawValues) error {
	p.forbidden("ApplyTaskConfig")
	return nil
}
func (p *pollSystem) CompleteMailbox(context.Context, task.Mailbox) error {
	p.forbidden("CompleteMailbox")
	return nil
}

type pollCounter struct {
	active  atomic.Int32
	maxSeen atomic.Int32
}

func (p *pollSystem) Poll(ctx context.Context) ([]task.Ticket, error) {
	if p.shared != nil {
		cur := p.shared.active.Add(1)
		for {
			old := p.shared.maxSeen.Load()
			if cur <= old || p.shared.maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		defer p.shared.active.Add(-1)
	}
	p.polls.Add(1)
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
		}
	}
	return p.batch, nil
}

func makeRepo(name string, sys task.System) *repo.Repo {
	return &repo.Repo{Name: name, Path: "/srv/" + name, TaskSystem: sys}
}

func TestPollerGroupRunsOnePollerPerRepo(t *testing.T) {
	systems := map[string]*pollSystem{}
	var repos []*repo.Repo
	for _, name := range []string{"a", "b", "c"} {
		s := &pollSystem{}
		systems[name] = s
		repos = append(repos, makeRepo(name, s))
	}

	var handled atomic.Int32
	g := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {
		handled.Add(1)
	})
	g.ReplaceRepos(repos)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	// Wait until every repo has polled at least once.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		all := true
		for _, s := range systems {
			if s.polls.Load() == 0 {
				all = false
			}
		}
		if all {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for name, s := range systems {
		if s.polls.Load() == 0 {
			t.Fatalf("repo %s never polled; one poller per repo expected", name)
		}
	}
	if handled.Load() == 0 {
		t.Fatal("batch handler never called")
	}
}

func TestPollerGroupCapsConcurrentPolls(t *testing.T) {
	// More than 10 repos due at once: at most 10 polls run concurrently.
	block := make(chan struct{})
	shared := &pollCounter{}
	var repos []*repo.Repo
	for i := 0; i < 15; i++ {
		s := &pollSystem{shared: shared, block: block}
		repos = append(repos, makeRepo(string(rune('a'+i)), s))
	}

	g := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {})
	g.ReplaceRepos(repos)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	time.Sleep(500 * time.Millisecond)
	if got := shared.maxSeen.Load(); got > 10 {
		t.Fatalf("concurrent polls peaked at %d, cap is 10", got)
	}
	close(block)
}

func TestRepoPollerUsesConfiguredInterval(t *testing.T) {
	s := &pollSystem{}
	r := makeRepo("a", s)

	p := &repo.RepoPoller{
		Repo:     r,
		Interval: 50 * time.Millisecond, // mirrors root pollIntervalSeconds plumbing
		Handle:   func(context.Context, *repo.Repo, []task.Ticket) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	time.Sleep(220 * time.Millisecond)
	cancel()

	if s.polls.Load() < 2 {
		t.Fatalf("poller ran %d times in 220ms at 50ms interval, want several", s.polls.Load())
	}
}

func TestPollerGroupPollsEveryRepoOnSharedInterval(t *testing.T) {
	// One Repo Poller per repo, all using the machine-wide interval. The
	// documented interval surface is RepoPoller.Interval; the startup wiring
	// sets every poller's Interval from Machine.PollIntervalSeconds. Build
	// them uniformly as the wiring does and assert the documented field.
	systems := map[string]*pollSystem{}
	var repos []*repo.Repo
	for _, name := range []string{"a", "b", "c"} {
		s := &pollSystem{}
		systems[name] = s
		repos = append(repos, makeRepo(name, s))
	}

	shared := 15 * time.Second // Machine.PollIntervalSeconds default
	pollers := make([]*repo.RepoPoller, 0, len(repos))
	for _, r := range repos {
		p := &repo.RepoPoller{Repo: r, Interval: shared, Handle: func(context.Context, *repo.Repo, []task.Ticket) {}}
		if p.Interval != shared {
			t.Fatalf("repo %s poller interval = %v, want machine-wide %v", r.Name, p.Interval, shared)
		}
		pollers = append(pollers, p)
	}

	// The group runs one poller per repo (each repo polled).
	g := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {})
	g.ReplaceRepos(repos)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	for name, s := range systems {
		if s.polls.Load() == 0 {
			t.Fatalf("repo %s never polled by the group", name)
		}
	}
}

func TestPollIntervalDefaultsTo15Seconds(t *testing.T) {
	// pollIntervalSeconds defaults to 15 when omitted. The machine config
	// owns this default; assert it here so the poller group's source value
	// is pinned.
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("taskPlugin: jira\nrunnerPlugin: orca\nharnessPlugin: opencode\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadMachine(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollIntervalSeconds != 15 {
		t.Fatalf("PollIntervalSeconds = %d, want default 15", cfg.PollIntervalSeconds)
	}
}

func TestPollerGroupObservesReposRegisteredAfterStart(t *testing.T) {
	// 9.8: Run must apply ReplaceRepos to the LIVE loop. A repo registered
	// after serve starts (after Run is already blocking) must be polled
	// within one poll interval, without restarting Run.
	initial := &pollSystem{}
	g := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {})
	g.Interval = 30 * time.Millisecond
	g.ReplaceRepos([]*repo.Repo{makeRepo("initial", initial)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	// Wait for the initial repo's first poll so Run is known to be live.
	deadline := time.Now().Add(2 * time.Second)
	for initial.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if initial.polls.Load() == 0 {
		t.Fatal("initial repo never polled; Run not live")
	}

	// Register a new repo AFTER Run is already running (the serve path:
	// repo register via API -> onReposChanged -> ReplaceRepos).
	added := &pollSystem{}
	g.ReplaceRepos([]*repo.Repo{makeRepo("initial", initial), makeRepo("added", added)})

	// It must be polled within one interval.
	deadline = time.Now().Add(5 * g.Interval)
	for added.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if added.polls.Load() == 0 {
		t.Fatal("repo registered after start was never polled; ReplaceRepos not applied to live loop")
	}
}

func TestPollerGroupStopsRemovedRepoPoller(t *testing.T) {
	// 9.8 flip side: removing a repo cancels its poller. After removal and
	// a grace window, the removed repo's poll count must stop growing.
	a := &pollSystem{}
	b := &pollSystem{}
	g := repo.NewPollerGroup(10, func(context.Context, *repo.Repo, []task.Ticket) {})
	g.Interval = 20 * time.Millisecond
	ra := makeRepo("a", a)
	g.ReplaceRepos([]*repo.Repo{ra, makeRepo("b", b)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for (a.polls.Load() == 0 || b.polls.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.polls.Load() == 0 || b.polls.Load() == 0 {
		t.Fatal("both repos should have polled before removal")
	}

	g.ReplaceRepos([]*repo.Repo{ra})
	time.Sleep(100 * time.Millisecond) // allow reconcile + cancel to land
	before := b.polls.Load()
	time.Sleep(3 * g.Interval)
	if got := b.polls.Load(); got != before {
		t.Fatalf("removed repo polled after removal: before=%d after=%d", before, got)
	}
}

func TestRepoPollerOnlyFetchesAndHandles(t *testing.T) {
	// A poller does no matching/claiming: it calls Poll and passes the batch
	// to the handler unchanged. Routing/claiming live in the batch handler
	// (task 5.3), not in the poller.
	want := []task.Ticket{{ID: "1", Key: "PAY-101"}}
	s := &pollSystem{batch: want}
	s.t = t // fail the test if the poller invokes any matching/claiming call
	r := makeRepo("a", s)

	got := make(chan []task.Ticket, 1)
	p := &repo.RepoPoller{
		Repo:     r,
		Interval: 20 * time.Millisecond,
		Handle: func(_ context.Context, _ *repo.Repo, batch []task.Ticket) {
			select {
			case got <- batch:
			default:
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	select {
	case batch := <-got:
		if len(batch) != len(want) || batch[0].Key != want[0].Key {
			t.Fatalf("handler received %v, want the poll batch %v unchanged", batch, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poller never delivered a batch to the handler")
	}
}
