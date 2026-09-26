package application

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestPhaseTraceRetainsRepeatedStagesAndCloses(t *testing.T) {
	clock := faketime.New(epoch)
	trace := phaseTrace{now: clock.Now}
	trace.change(PhaseAssociation)
	clock.Advance(1500 * time.Millisecond)
	trace.change(PhaseAddress)
	clock.Advance(2 * time.Second)
	trace.change(PhaseAssociation)
	clock.Advance(500 * time.Millisecond)
	trace.change(PhaseAssociation) // repeated polling is not another visit
	trace.change(Phase("untrusted diagnostic"))
	got := trace.finish()
	want := []PhaseTiming{{Phase: PhaseAssociation, Milliseconds: 2000, Visits: 2}, {Phase: PhaseAddress, Milliseconds: 2000, Visits: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("timings=%+v", got)
	}
	got[0].Milliseconds = 999
	clock.Advance(time.Second)
	trace.change(PhaseLogin)
	if !slices.Equal(trace.finish(), want) {
		t.Fatal("closed trace changed or shared backing storage")
	}
}

func TestWorkerTimingSurvivesCompletionAndCannotBeMutatedByReader(t *testing.T) {
	h := newHarness(t, nil)
	id := h.submit(KindSwitchCampus, "campus", "timed-switch").ActionID
	h.awaitStart()
	h.reportPhase(id, PhasePrepare)
	h.awaitPhase(id, PhasePrepare)
	h.clock.Advance(100 * time.Millisecond)
	h.reportPhase(id, PhaseActivate)
	h.awaitPhase(id, PhaseActivate)
	h.clock.Advance(700 * time.Millisecond)
	h.reportPhase(id, PhaseAssociation)
	h.awaitPhase(id, PhaseAssociation)
	h.clock.Advance(2 * time.Second)
	h.succeed(id)
	final := h.awaitState(id, StateSucceeded)
	if final.WorkerMilliseconds != 2800 {
		t.Fatalf("worker duration=%d", final.WorkerMilliseconds)
	}
	want := []PhaseTiming{{Phase: PhaseWaitingLink, Visits: 1}, {Phase: PhasePrepare, Milliseconds: 100, Visits: 1}, {Phase: PhaseActivate, Milliseconds: 700, Visits: 1}, {Phase: PhaseAssociation, Milliseconds: 2000, Visits: 1}}
	if !slices.Equal(final.Timings, want) {
		t.Fatalf("timings=%+v", final.Timings)
	}
	final.Timings[0].Milliseconds = 12345
	if !slices.Equal(h.state(id).Timings, want) {
		t.Fatal("reader changed retained timing")
	}
}

func TestDeviceProgressUsesWorkerContextAndTraceHasFiniteStages(t *testing.T) {
	clock := faketime.New(epoch)
	trace := phaseTrace{now: clock.Now}
	ctx := context.WithValue(t.Context(), phaseReporterKey{}, func(phase Phase) { trace.change(phase) })
	for range 1000 {
		ReportPhase(ctx, PhaseAssociation)
		ReportPhase(ctx, PhaseAddress)
		ReportPhase(ctx, Phase("ignored"))
	}
	got := trace.finish()
	if len(got) != 2 || got[0].Visits != 1000 || got[1].Visits != 1000 {
		t.Fatal("trace retained every poll or accepted arbitrary stage")
	}
	ReportPhase(t.Context(), PhaseRollback) // direct adapter tests need no coordinator
}
