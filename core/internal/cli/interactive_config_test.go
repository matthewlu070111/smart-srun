//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type scriptedForm struct {
	t       *testing.T
	answers []string
	secrets []bool
	hook    func(string)
}

func (f *scriptedForm) Read(_ context.Context, label string, secret bool, _ int) (string, error) {
	f.t.Helper()
	if f.hook != nil {
		f.hook(label)
	}
	if len(f.answers) == 0 {
		f.t.Fatalf("unexpected prompt: %s", label)
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	f.secrets = append(f.secrets, secret)
	return answer, nil
}

func TestInteractiveConfigPreservesSecretSemantics(t *testing.T) {
	for _, editing := range []bool{false, true} {
		t.Run(map[bool]string{false: "add", true: "edit"}[editing], func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Revision = 42
			cfg.CampusAccounts = []domain.CampusAccount{{ID: "c1", Label: "existing", UserID: "student", OperatorSuffix: "known", AccessMode: domain.AccessModeWired, WiredIface: "wan"}}
			cfgJSON, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := onlineClient{ensure: func(context.Context) error { return nil }, call: func(_ context.Context, method string, params any) (json.RawMessage, error) {
				if method == "config.get" {
					return cfgJSON, nil
				}
				calls++
				if method != "campus.upsert" {
					t.Fatal(method)
				}
				p := params.(daemon.CampusUpsertParams)
				if p.ExpectedRevision == nil || *p.ExpectedRevision != 42 {
					t.Fatal("lost revision")
				}
				if editing {
					if p.Account.Password != nil || p.Account.ID != "c1" || *p.Account.OperatorSuffix != "known" {
						t.Fatal("edit overwrote omitted password or suffix")
					}
				} else if p.Account.Password == nil || *p.Account.Password != " exact'秘密\\value " || *p.Account.OperatorSuffix != "" {
					t.Fatal("new password or empty suffix changed")
				}
				return json.RawMessage(`{"ok":true}`), nil
			}}
			args := []string{"account", "add"}
			answers := []string{"label", "student", " exact'秘密\\value ", "", "wired", "wan", "http://portal.example", "1", "y"}
			if editing {
				args = []string{"account", "edit", "c1"}
				answers = []string{"", "", "", "", "", "", "", "", "y"}
			}
			form := &scriptedForm{t: t, answers: answers}
			code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
				return interactiveConfig(t.Context(), client, args, form, stdout, stderr)
			})
			if code != ExitOK || calls != 1 || len(form.answers) != 0 {
				t.Fatalf("code=%d calls=%d remaining=%d stderr=%s", code, calls, len(form.answers), errOut)
			}
			if strings.Contains(out+errOut, "秘密") {
				t.Fatal("secret printed")
			}
			for i, secret := range form.secrets {
				if secret != (i == 2) {
					t.Fatalf("secret flag for prompt %d = %v", i, secret)
				}
			}
		})
	}
}

func TestInteractiveConfigCancelAndConcurrentEditNeverOverwrite(t *testing.T) {
	for _, mode := range []string{"decline", "revision-conflict"} {
		t.Run(mode, func(t *testing.T) {
			client, _ := onlineDaemon(t)
			form := &scriptedForm{t: t, answers: []string{"Phone", "exact SSID", "", "psk2", " synthetic-wifi-key ", "y"}}
			if mode == "decline" {
				form.answers[len(form.answers)-1] = ""
			} else {
				form.hook = func(label string) {
					if !strings.HasPrefix(label, "保存以上") {
						return
					}
					revision := uint64(0)
					_, err := client.call(t.Context(), "config.apply", daemon.ConfigApplyParams{ExpectedRevision: &revision, Settings: json.RawMessage(`{"enabled":false}`)})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			code, out, errOut := capture(t, func(stdout, stderr *os.File) int {
				return interactiveConfig(t.Context(), client, []string{"hotspot", "add"}, form, stdout, stderr)
			})
			if code == ExitOK || strings.Contains(out+errOut, "synthetic-wifi-key") {
				t.Fatalf("code=%d leaked=%v", code, strings.Contains(out+errOut, "synthetic-wifi-key"))
			}
			cfg, err := readOnlineConfig(t.Context(), client)
			if err != nil || len(cfg.HotspotProfiles) != 0 {
				t.Fatal("cancelled/stale form changed hotspots", err)
			}
			want := uint64(0)
			if mode == "revision-conflict" {
				want = 1
			}
			if cfg.Revision != want {
				t.Fatalf("revision %d want %d", cfg.Revision, want)
			}
		})
	}
}
