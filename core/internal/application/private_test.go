package application

import (
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestWizardDraftOnlyReachesWorkerAndIsErasedAfterCompletion(t *testing.T) {
	h := newHarness(t, nil)
	request := Request{Kind: KindDetectVerify, Interface: "wan", ProbeURL: "http://portal.invalid", IdempotencyKey: "private", PrivateJSON: `{"password":"draft-secret"}`}
	receipt, err := h.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if h.awaitStart().Request.PrivateJSON != request.PrivateJSON {
		t.Fatal("worker lost immutable draft")
	}
	if h.state(receipt.ActionID).Request.PrivateJSON != "" || h.list()[0].Request.PrivateJSON != "" {
		t.Fatal("reader received password")
	}
	h.succeed(receipt.ActionID)
	h.awaitState(receipt.ActionID, StateSucceeded)
	duplicate, err := h.Submit(t.Context(), request)
	if err != nil || !duplicate.Duplicate || duplicate.ActionID != receipt.ActionID {
		t.Fatal("erasure broke duplicate handling", err)
	}
	request.PrivateJSON = `{"password":"changed-secret"}`
	if codeOf(t, h.submitExpectingError(request)) != domain.CodeConflict {
		t.Fatal("changed password reused old result")
	}
	h.shutdown()
	if h.index[receipt.ActionID].Request.PrivateJSON != "" {
		t.Fatal("terminal history retained draft")
	}
	for _, event := range h.seen {
		if event.Request.PrivateJSON != "" {
			t.Fatal("observer received password")
		}
	}
}

func TestQueuedWizardCancellationErasesDraftWithoutDispatch(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Parallel = 1 })
	h.submit(KindLogin, "campus", "block")
	h.awaitStart()
	request := Request{Kind: KindDetectVerify, Interface: "wan", ProbeURL: "http://portal.invalid", IdempotencyKey: "private", PrivateJSON: `{"password":"draft-secret"}`}
	r, err := h.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Cancel(t.Context(), r.ActionID); err != nil {
		t.Fatal(err)
	}
	h.expectNoStart("cancelled draft")
	h.shutdown()
	if h.index[r.ActionID].Request.PrivateJSON != "" {
		t.Fatal("cancelled history retained draft")
	}
	request.PrivateJSON = strings.Repeat("x", (16<<10)+1)
	if request.Validate() == nil {
		t.Fatal("unbounded private input")
	}
}
