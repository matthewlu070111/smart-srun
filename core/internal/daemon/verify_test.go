//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
)

type wizardLine struct{ *http.Client }

func (wizardLine) SourceAddr() netip.Addr { return netip.MustParseAddr("192.0.2.3") }

func wizardService(t *testing.T, handler http.HandlerFunc) (*running, VerificationParams) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	service := start(t, func(o *Options) {
		o.OpenProbe = func(context.Context, string) (portal.Fetcher, func(), error) {
			return wizardLine{server.Client()}, func() {}, nil
		}
	})
	return service, VerificationParams{DetectACIDParams: DetectACIDParams{BaseURL: server.URL + "/login", ACID: "9", Iface: "wan", AccessMode: "wired", IdempotencyKey: "wizard", Session: "wizard-session"}, UserID: "student"}
}

func wizardResult(t *testing.T, service *running, method string, p VerificationParams) (SubmitResult, VerificationResult, application.Action) {
	t.Helper()
	var receipt SubmitResult
	if err := json.Unmarshal(service.call(method, p), &receipt); err != nil {
		t.Fatal(err)
	}
	action := service.awaitTerminal(receipt.ActionID)
	var view struct {
		Result VerificationResult `json:"result"`
	}
	raw := service.call("action.get", ActionParams{ActionID: receipt.ActionID, Session: p.Session})
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if action.Request.PrivateJSON != "" || strings.Contains(string(raw), "draft-secret") {
		t.Fatal("task exposed private draft")
	}
	if _, err := os.Stat(service.paths.ConfigFile()); !os.IsNotExist(err) {
		t.Fatal("probe wrote saved configuration")
	}
	return receipt, view.Result, action
}

func TestWizardOnlineIdentityNeverAuthenticates(t *testing.T) {
	for _, test := range []struct {
		name, response string
		confirmed      bool
		suffix         string
	}{
		{"realm", `{"user_name":"student","domain":"Custom.REALM"}`, true, "Custom.REALM"},
		{"bare", `{"user_name":"student"}`, false, ""},
		{"empty_realm", `{"user_name":"student@"}`, false, ""},
		{"foreign", `{"user_name":"other@cmcc"}`, false, ""},
		{"offline", `{"error":"not_online"}`, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			service, p := wizardService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/cgi-bin/rad_user_info" || r.URL.Query().Get("password") != "" {
					t.Error("identity attempted credentialed request")
				}
				io.WriteString(w, test.response)
			})
			_, result, action := wizardResult(t, service, "detect.identity", p)
			if action.State != application.StateSucceeded || result.Confirmed != test.confirmed || result.Suffix != test.suffix || result.PasswordVerified || calls.Load() != 1 {
				t.Fatalf("identity: %+v / %s", result, action.State)
			}
			if test.name != "offline" {
				p.IdempotencyKey = "active"
				p.Password = "draft-secret"
				p.Candidates = []string{"ctcc"}
				_, result, action = wizardResult(t, service, "detect.verify", p)
				if action.State != application.StateSucceeded || len(result.Attempts) != 0 || result.PasswordVerified || calls.Load() != 2 {
					t.Fatal("online session was touched")
				}
			}
		})
	}
}

func TestWizardVerificationRequiresExplicitCandidateAndExactIdentity(t *testing.T) {
	for _, test := range []struct {
		name, login, reported, outcome string
		confirmed, verified            bool
	}{
		{"accepted", `{"error":"ok"}`, "student@custom", "hit", true, true},
		{"bare_is_insufficient", `{"error":"ok"}`, "student", "other", false, false},
		{"already_online", `{"error":"ip_already_online_error"}`, "student@custom", "hit", true, false},
		{"bad_password", `{"error":"login_error","error_msg":"password error"}`, "", "credential", false, false},
		{"limited", `{"error":"E2532"}`, "", "limited", false, false},
		{"unknown", `{"error":"backend_busy","error_msg":"draft-secret"}`, "", "other", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var online, login atomic.Int32
			service, p := wizardService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cgi-bin/rad_user_info":
					if online.Add(1) == 1 {
						io.WriteString(w, `{"error":"not_online"}`)
					} else {
						json.NewEncoder(w).Encode(map[string]string{"user_name": test.reported})
					}
				case "/cgi-bin/get_challenge":
					io.WriteString(w, `{"challenge":"synthetic-token","client_ip":"192.0.2.3"}`)
				case "/cgi-bin/srun_portal":
					login.Add(1)
					q := r.URL.Query()
					if q.Get("action") != "login" || q.Get("username") != "student@custom" || !strings.HasPrefix(q.Get("password"), "{MD5}") || q.Get("ac_id") != "9" {
						t.Error("incorrect authentication wire fields")
					}
					io.WriteString(w, test.login)
				default:
					t.Error("unexpected request, including logout", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			p.Password = "draft-secret"
			p.Candidates = []string{"custom", "unused"}
			receipt, result, action := wizardResult(t, service, "detect.verify", p)
			if action.State != application.StateSucceeded || result.Confirmed != test.confirmed || result.PasswordVerified != test.verified || len(result.Attempts) != 1 || result.Attempts[0].Outcome != test.outcome || login.Load() != 1 {
				t.Fatalf("verification: %+v / %s", result, action.State)
			}
			var again SubmitResult
			json.Unmarshal(service.call("detect.verify", p), &again)
			if !again.Duplicate || again.ActionID != receipt.ActionID {
				t.Fatal("completed duplicate authenticated again")
			}
			p.Password = "changed-secret"
			service.callExpectingError("detect.verify", p)
		})
	}
}

func TestWizardInvalidOrUnknownInputNeverAuthenticates(t *testing.T) {
	var calls atomic.Int32
	service, p := wizardService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/cgi-bin/rad_user_info" {
			t.Error("unknown preflight allowed login")
		}
		io.WriteString(w, `{"error":"backend_busy"}`)
	})
	p.Password = "draft-secret"
	p.Candidates = []string{""}
	service.callExpectingError("detect.identity", p)
	_, _, action := wizardResult(t, service, "detect.verify", p)
	if action.State != application.StateFailed || calls.Load() != 1 {
		t.Fatal("unknown status must stop")
	}
	p.IdempotencyKey = "too-many"
	p.Candidates = []string{"a", "b", "c", "d", "e", "f"}
	service.callExpectingError("detect.verify", p)
	p.Candidates = []string{"??"}
	service.callExpectingError("detect.verify", p)
	p.Candidates = []string{""}
	p.Iface = ""
	service.callExpectingError("detect.verify", p)
	if calls.Load() != 1 {
		t.Fatal("invalid input performed I/O")
	}
}

func TestWizardOnlyRetriesExplicitMissAndCancellationStopsNextCandidate(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "next_empty_suffix", true: "cancel_during_cooldown"}[cancel], func(t *testing.T) {
			var logins, online atomic.Int32
			first := make(chan struct{})
			service, p := wizardService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/cgi-bin/rad_user_info":
					if online.Add(1) == 1 {
						io.WriteString(w, `{"error":"not_online"}`)
					} else {
						io.WriteString(w, `{"user_name":"student"}`)
					}
				case "/cgi-bin/get_challenge":
					io.WriteString(w, `{"challenge":"synthetic-token"}`)
				case "/cgi-bin/srun_portal":
					if logins.Add(1) == 1 {
						io.WriteString(w, `{"error":"E2531"}`)
						close(first)
					} else {
						if r.URL.Query().Get("username") != "student" {
							t.Error("explicit empty suffix lost")
						}
						io.WriteString(w, `{"error":"ok"}`)
					}
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			})
			p.Password = "draft-secret"
			p.Candidates = []string{"missing", "missing", ""}
			var receipt SubmitResult
			json.Unmarshal(service.call("detect.verify", p), &receipt)
			select {
			case <-first:
			case <-time.After(patience):
				t.Fatal("no login")
			}
			if cancel {
				service.call("action.cancel", ActionParams{ActionID: receipt.ActionID, Session: p.Session})
			}
			action := service.awaitTerminal(receipt.ActionID)
			if cancel {
				if action.State != application.StateCancelled || action.ResultJSON != "" || logins.Load() != 1 {
					t.Fatal("cancel authenticated next candidate")
				}
			} else {
				var result VerificationResult
				json.Unmarshal([]byte(action.ResultJSON), &result)
				if action.State != application.StateSucceeded || !result.Confirmed || result.Suffix != "" || !result.PasswordVerified || len(result.Attempts) != 2 || logins.Load() != 2 {
					t.Fatalf("explicit candidate fallback: %+v", result)
				}
			}
		})
	}
}
