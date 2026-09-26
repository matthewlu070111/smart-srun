package application

import "sort"

type maintenanceResult struct {
	action Action
	order  uint64
}

// Observe never waits for the scheduling loop. An undelivered terminal result
// must survive a burst: dropping it leaves inFlight set forever. There can be
// only one owned action of each kind per account; a newer one means the loop
// already consumed or invalidated the old one. Configured accounts bound the
// mailbox (four automatic kinds per account, plus the latest manual switch).
func (m *Maintainer) Observe(action Action) {
	if !action.State.Terminal() {
		return
	}
	key := ""
	switch action.Request.Kind {
	case KindMaintain, KindForcedLogout, KindQuietHotspot, KindQuietCampus:
		key = string(action.Request.Kind) + ":" + action.Request.AccountID
	case KindSwitchCampus, KindSwitchHotspot:
		if action.State == StateSucceeded {
			key = "manual-switch"
		}
	case KindLogin, KindRelogin, KindLogout:
		// These can change manual pause intent. Wake the loop without retaining
		// unrelated per-click history or spending an automatic result's slot.
	default:
		return
	}
	if key != "" {
		cfg := m.settings.Snapshot()
		if key != "manual-switch" {
			if _, known := cfg.CampusAccountByID(action.Request.AccountID); !known {
				return
			}
			if action.Request.CheckRevision && action.Request.ConfigRevision != cfg.Revision {
				return
			}
		}
		m.resultsMu.Lock()
		// Removed accounts and old revisions must not accumulate during rapid
		// configuration edits while the loop is waiting for Submit to return.
		for slot, previous := range m.results {
			if slot == "manual-switch" {
				continue
			}
			r := previous.action.Request
			_, known := cfg.CampusAccountByID(r.AccountID)
			if !known || (r.CheckRevision && r.ConfigRevision != cfg.Revision) {
				delete(m.results, slot)
			}
		}
		previous, exists := m.results[key]
		// A cancelled worker can publish its timings after a newer worker has
		// finished. Submission ordinal keeps that late copy from replacing it.
		if !exists || previous.action.ordinal <= action.ordinal {
			m.resultOrder++
			action.Timings, action.ResultJSON = nil, ""
			m.results[key] = maintenanceResult{action: action, order: m.resultOrder}
		}
		m.resultsMu.Unlock()
	}
	select {
	case m.resultReady <- struct{}{}:
	default:
	}
}

func (m *Maintainer) takeResults() []Action {
	m.resultsMu.Lock()
	pending := make([]maintenanceResult, 0, len(m.results))
	for _, result := range m.results {
		pending = append(pending, result)
	}
	clear(m.results)
	m.resultsMu.Unlock()
	// Keep effective publication order between manual choices and scheduled
	// results, even though each individual slot coalesces redundant history.
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	result := make([]Action, 0, len(pending))
	for _, value := range pending {
		result = append(result, value.action)
	}
	return result
}
