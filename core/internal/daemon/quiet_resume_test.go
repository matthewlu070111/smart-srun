//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/policy/faketime"
)

func TestQuietDeadlineSurvivesServiceRestartAndManualHotspotChoice(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "scheduled return"
		if manual {
			name = "manual hotspot during quiet retains morning return"
		}
		t.Run(name, func(t *testing.T) {
			clock := faketime.New(time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
			r := start(t, func(o *Options) {
				o.Clock, o.Runner = clock, switchRunner{}
				// This test starts after the default daily preset refresh time.
				// Disable that unrelated job before Run starts: otherwise its
				// first snapshot can race with account setup and block the next
				// config write while the refresh action is in flight.
				cfg := config.Defaults()
				cfg.PresetUpdates.Enabled = false
				data, err := config.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(o.Paths.Config, config.DirMode); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(o.Paths.ConfigFile(), data, config.FileMode); err != nil {
					t.Fatal(err)
				}
			})
			r.writeConfig("campus.upsert", `{"expected_revision":0,"account":{"user_id":"student","password":"private-password","wired_iface":"wan"}}`)
			r.writeConfig("hotspot.upsert", `{"expected_revision":1,"profile":{"ssid":"phone","radio":"radio0","encryption":"none"}}`)
			r.writeConfig("config.apply", `{"expected_revision":2,"settings":{"enabled":true,"quiet":{"enabled":true,"start":"20:00","end":"21:00","force_logout":true},"failover":{"enabled":true}}}`)
			awaitKind := func(service *running, kind application.Kind) {
				t.Helper()
				deadline := time.After(patience)
				for {
					select {
					case action := <-service.actions:
						if action.State.Terminal() && action.Request.Kind == kind {
							if action.State != application.StateSucceeded {
								t.Fatalf("transition failed: %+v", action)
							}
							return
						}
					case <-deadline:
						t.Fatalf("%s never completed", kind)
					}
				}
			}
			awaitKind(r, application.KindQuietHotspot)
			if marker, err := readQuietResume(r.paths); err != nil || marker == nil || marker.AccountID != "c1" || marker.HotspotID != "h1" {
				t.Fatalf("completed switch did not retain ownership: %+v / %v", marker, err)
			}
			if manual {
				// Choosing the same hotspot does not write config, but the
				// enabled timetable still applies at the morning boundary.
				var receipt SubmitResult
				if err := json.Unmarshal(r.call("action.submit", SubmitParams{Kind: "switch_hotspot", HotspotID: "h1", IdempotencyKey: "stay-here"}), &receipt); err != nil {
					t.Fatal(err)
				}
				if action := r.awaitTerminal(receipt.ActionID); action.State != application.StateSucceeded {
					t.Fatalf("manual switch failed: %+v", action)
				}
				if marker, err := readQuietResume(r.paths); err != nil || marker == nil || !marker.SweepPending || marker.Revision != 3 {
					t.Fatalf("manual choice lost the return or invented a completed logout: %+v / %v", marker, err)
				}
			}
			r.stop()
			if err := r.wait(); err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Hour)
			restarted := start(t, func(o *Options) { o.Paths, o.Clock, o.Runner = r.paths, clock, switchRunner{} })
			restarted.paths, restarted.client = r.paths, control.Client{Path: r.paths.Socket()}
			awaitKind(restarted, application.KindQuietCampus)
			if marker, err := readQuietResume(r.paths); err != nil || marker != nil {
				t.Fatalf("ownership not released: %+v / %v", marker, err)
			}
			cfg, err := config.LoadFile(r.paths.ConfigFile())
			if err != nil || cfg.Revision != 3 {
				t.Fatal("temporary scheduling wrote persistent config", err)
			}
		})
	}
}

func TestQuietOwnershipRecordIsPrivateBoundedAndRemovable(t *testing.T) {
	paths := Paths{Runtime: t.TempDir()}
	if record, err := readQuietResume(paths); record != nil || err != nil {
		t.Fatalf("missing record: %v / %v", record, err)
	}
	want := application.QuietResume{Revision: 3, Occurrence: "test-window", AccountID: "c1", HotspotID: "h1", StartedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}
	if err := writeQuietResume(paths, want); err != nil {
		t.Fatal(err)
	}
	got, err := readQuietResume(paths)
	if err != nil || got == nil || *got != want {
		t.Fatalf("record did not round-trip: %+v / %v", got, err)
	}
	for _, bad := range []string{`{"schema_version":2}`, `{"schema_version":1,"schema_version":1}`, `{"schema_version":1,"password":"never accepted"}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(paths.QuietResume(), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := readQuietResume(paths); got != nil || err == nil {
			t.Fatal("invalid ownership record accepted")
		}
	}
	if err := clearQuietResume(paths); err != nil {
		t.Fatal(err)
	}
	if err := clearQuietResume(paths); err != nil {
		t.Fatal("repeated revocation failed")
	}
}

func TestQuietOwnershipDoesNotFollowLinksOrReadPublicFiles(t *testing.T) {
	paths := Paths{Runtime: t.TempDir()}
	other := t.TempDir() + "/other"
	if err := os.WriteFile(other, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, paths.QuietResume()); err != nil {
		t.Fatal(err)
	}
	if _, err := readQuietResume(paths); err == nil {
		t.Fatal("followed a link")
	}
	if err := clearQuietResume(paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("removed the linked target")
	}
	if err := os.WriteFile(paths.QuietResume(), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readQuietResume(paths); err == nil {
		t.Fatal("accepted non-private ownership")
	}
}
