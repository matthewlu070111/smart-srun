package application

import (
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

func TestObservedLinkLossRetiresPoolBeforeSameAddressRecovery(t *testing.T) {
	for _, duringAttempt := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-attempt", true: "during-verification"}[duringAttempt], func(t *testing.T) {
			p := newPortal(t)
			binder := &fakeBinder{linkState: domain.LinkDown}
			worker, direct := workerFor(t, p, binder)
			lines := &pooledFixture{direct: direct, pool: transport.NewPool()}
			t.Cleanup(lines.pool.Close)
			worker.lines = lines
			first := runWorker(t, worker, KindLogin)
			if first.State != StateSucceeded || lines.pool.Len() != 1 {
				t.Fatalf("initial login: %+v", first)
			}
			loseLink := func() {
				binder.mu.Lock()
				binder.err = domain.Errorf(domain.CodeBindingUnavailable, "synthetic link down")
				binder.mu.Unlock()
			}
			if duringAttempt {
				p.mu.Lock()
				p.onlineAfter = func(int) { loseLink() }
				p.mu.Unlock()
			} else {
				loseLink()
			}
			failed := runWorker(t, worker, KindLogin)
			if failed.State != StateFailed || failed.Code != domain.CodeBindingUnavailable {
				t.Fatalf("link loss accepted: %+v", failed)
			}
			if lines.pool.Len() != 0 {
				t.Error("observed link loss left the old client in the pool")
			}
			binder.mu.Lock()
			binder.err = nil
			binder.mu.Unlock()
			p.mu.Lock()
			p.onlineAfter = nil
			p.mu.Unlock()
			recovered := runWorker(t, worker, KindLogin)
			if recovered.State != StateSucceeded {
				t.Fatalf("same-address recovery failed: %+v", recovered)
			}
			if recovered.Observation.Generation <= first.Observation.Generation || lines.clients[0] == lines.clients[len(lines.clients)-1] {
				t.Error("same-address reconnect reused the disconnected generation/client")
			}
		})
	}
}

func TestLateLinkInvalidationDoesNotRetireNewerBinding(t *testing.T) {
	p := newPortal(t)
	binder := &fakeBinder{}
	worker, direct := workerFor(t, p, binder)
	lines := &pooledFixture{direct: direct, pool: transport.NewPool()}
	t.Cleanup(lines.pool.Close)
	worker.lines = lines
	first := runWorker(t, worker, KindLogin)
	if first.State != StateSucceeded {
		t.Fatalf("initial login: %+v", first)
	}
	worker.invalidateLine("c1", first.Observation.Generation)
	second := runWorker(t, worker, KindLogin)
	if second.State != StateSucceeded {
		t.Fatalf("recovery: %+v", second)
	}
	worker.invalidateLine("c1", first.Observation.Generation)
	third := runWorker(t, worker, KindLogin)
	if third.State != StateSucceeded || third.Observation.Generation != second.Observation.Generation || lines.clients[1] != lines.clients[2] {
		t.Fatal("late invalidation discarded the replacement binding")
	}
}

func TestDNSChangeInvalidatesAttemptAndRetiresItsClient(t *testing.T) {
	p := newPortal(t)
	binder := &fakeBinder{}
	worker, direct := workerFor(t, p, binder)
	lines := &pooledFixture{direct: direct, pool: transport.NewPool()}
	t.Cleanup(lines.pool.Close)
	worker.lines = lines
	prepared, failed := worker.prepare(t.Context(), Action{Request: Request{Kind: KindLogin, AccountID: "c1"}}, func(Phase) {})
	if prepared == nil {
		t.Fatalf("prepare: %+v", failed)
	}
	changed := steadyBinding()
	changed.DNSServers = []netip.Addr{netip.MustParseAddr("192.0.2.53")}
	binder.bindings = []domain.Binding{changed}
	err := worker.confirmUnchanged(t.Context(), prepared)
	if code, _ := domain.CodeOf(err); code != domain.CodeBindingChanged {
		t.Fatalf("DNS changed during an attempt without invalidation: %v", err)
	}
	if lines.pool.Len() != 0 {
		t.Fatal("changed resolver retained the old client")
	}
}
