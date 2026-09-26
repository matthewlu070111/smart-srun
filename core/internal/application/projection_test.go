package application

import (
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/observe"
)

// collector is a Record hook a test can read back.
type collector struct {
	mu   sync.Mutex
	seen []observe.Observation
}

func (c *collector) record(observation observe.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, observation)
}

func (c *collector) all() []observe.Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]observe.Observation(nil), c.seen...)
}

// await waits for the projection to have been told something.
//
// A round-trip through the loop proves the loop ran, but not that it chose the
// completion out of its select rather than this very round-trip, so a fixed
// number of round-trips is a coin toss rather than a synchronisation. Waiting
// for the thing itself is the only version that is not.
func (c *collector) await(t *testing.T, h *harness, want int) []observe.Observation {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		if got := c.all(); len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the projection received %d observations, want %d",
				len(c.all()), want)
		}
		h.settle()
	}
}

// What an attempt learned reaches the projection.
//
// The worker does not write it itself. Spec 02 makes the coordinator the one
// place that decides whether a worker's answer is still current, and a worker
// reaching into the store from its own goroutine would be a second writer --
// the exact thing the single-writer loop exists to prevent.
func TestWhatAnAttemptLearnedReachesTheProjection(t *testing.T) {
	sink := &collector{}
	h := newHarness(t, func(options *Options) { options.Record = sink.record })

	receipt := h.submit(KindLogin, "c1", "k1")
	h.awaitStart()
	h.finish(receipt.ActionID, Outcome{
		State:   StateSucceeded,
		Message: "认证完成",
		Observation: &observe.Observation{
			AccountID: "c1",
			Auth:      domain.AuthVerifiedSelf,
			Sequence:  1,
		},
	})
	h.awaitState(receipt.ActionID, StateSucceeded)

	got := sink.await(t, h, 1)
	if got[0].AccountID != "c1" || got[0].Auth != domain.AuthVerifiedSelf {
		t.Errorf("observation = %+v", got[0])
	}
}

// A result carrying nothing about the line does not manufacture an entry.
//
// Most outcomes have no observation -- an unknown account, a refused request --
// and recording a zero-valued one would put "unknown, no address, nobody
// online" into the projection as though it had been measured.
func TestAResultWithNothingLearnedRecordsNothing(t *testing.T) {
	sink := &collector{}
	h := newHarness(t, func(options *Options) { options.Record = sink.record })

	receipt := h.submit(KindLogin, "c1", "k1")
	h.awaitStart()
	h.finish(receipt.ActionID, Outcome{State: StateFailed,
		Code: domain.CodeNotFound, Message: "账号不存在"})
	h.awaitState(receipt.ActionID, StateFailed)

	if got := sink.all(); len(got) != 0 {
		t.Fatalf("the projection received %+v for a result that learned nothing", got)
	}
}

// A cancelled attempt still learned something, and it still travels.
//
// It found out whether the line had an address and whose session was on it.
// Throwing that away because the user pressed stop leaves the status page
// showing something older and less true. Whether the observation is still
// current is observe's decision -- it holds the revision, generation and
// sequence rules -- and deciding it a second time here is how two answers to
// one question start to differ.
func TestACancelledAttemptStillReportsWhatItLearned(t *testing.T) {
	sink := &collector{}
	h := newHarness(t, func(options *Options) { options.Record = sink.record })

	receipt := h.submit(KindLogin, "c1", "k1")
	h.awaitStart()
	h.lingerOn(receipt.ActionID)
	h.cancel(receipt.ActionID)
	h.awaitCancelSeen(receipt.ActionID)

	h.finish(receipt.ActionID, Outcome{
		State: StateFailed,
		Observation: &observe.Observation{
			AccountID: "c1",
			Link:      domain.LinkReady,
			Auth:      domain.AuthUnknown,
		},
	})
	h.settle()

	got := sink.await(t, h, 1)
	if got[0].Link != domain.LinkReady {
		t.Errorf("observation = %+v", got[0])
	}
	// The action itself stays cancelled: the worker's verdict does not reopen a
	// terminal state, which is a separate rule from the one above.
	if state := h.state(receipt.ActionID).State; state != StateCancelled {
		t.Errorf("action state = %s, want it to stay cancelled", state)
	}
}
