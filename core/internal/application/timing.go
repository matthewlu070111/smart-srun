package application

import (
	"context"
	"sync"
	"time"
)

const (
	PhasePrepare     Phase = "prepare"
	PhaseActivate    Phase = "activate"
	PhaseAssociation Phase = "association"
	PhaseAddress     Phase = "address"
	PhaseCommit      Phase = "commit"
	PhaseRollback    Phase = "rollback"
	PhaseRetire      Phase = "retire"
	PhaseReuse       Phase = "reuse"
)

// PhaseTiming is worker time, not a measurement of application traffic loss.
// Repeated visits are aggregated, with a fixed upper bound on retained stages.
type PhaseTiming struct {
	Phase        Phase  `json:"phase"`
	Milliseconds int64  `json:"milliseconds"`
	Visits       uint32 `json:"visits"`
}

type phaseReporterKey struct{}

// ReportPhase lets device adapters refine progress without owning action state.
func ReportPhase(ctx context.Context, phase Phase) {
	if report, ok := ctx.Value(phaseReporterKey{}).(func(Phase)); ok {
		report(phase)
	}
}

func knownPhase(phase Phase) bool {
	switch phase {
	case PhaseWaitingLink, PhaseChallenge, PhaseLogin, PhaseVerify, PhaseLogout, PhaseSwitch, PhaseFetch,
		PhasePrepare, PhaseActivate, PhaseAssociation, PhaseAddress, PhaseCommit, PhaseRollback, PhaseRetire, PhaseReuse:
		return true
	}
	return false
}

type phaseTrace struct {
	mu        sync.Mutex
	now       func() time.Time
	since     time.Time
	current   Phase
	closed    bool
	entries   []PhaseTiming
	durations []time.Duration
}

func (p *phaseTrace) change(phase Phase) {
	if !knownPhase(phase) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.current == phase {
		return
	}
	at := p.now()
	p.accrue(at)
	p.current, p.since = phase, at
	for i := range p.entries {
		if p.entries[i].Phase == phase {
			p.entries[i].Visits++
			return
		}
	}
	p.entries = append(p.entries, PhaseTiming{Phase: phase, Visits: 1})
	p.durations = append(p.durations, 0)
}

func (p *phaseTrace) accrue(at time.Time) {
	if p.current == "" {
		return
	}
	elapsed := at.Sub(p.since)
	if elapsed < 0 {
		elapsed = 0
	}
	for i := range p.entries {
		if p.entries[i].Phase == p.current {
			p.durations[i] += elapsed
			return
		}
	}
}

func (p *phaseTrace) finish() []PhaseTiming {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.accrue(p.now())
		p.closed = true
		for i := range p.entries {
			p.entries[i].Milliseconds = p.durations[i].Milliseconds()
		}
	}
	return append([]PhaseTiming(nil), p.entries...)
}
