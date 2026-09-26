package control

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type frameFixtures struct {
	RPCVersion    int `json:"rpc_version"`
	ValidRequests []struct {
		Name   string `json:"name"`
		Method string `json:"method"`
		Line   string `json:"line"`
	} `json:"valid_requests"`
	InvalidRequests []struct {
		Name       string `json:"name"`
		ExpectCode string `json:"expect_code"`
		Line       string `json:"line"`
	} `json:"invalid_requests"`
	UndeclaredMethods []struct {
		Name       string `json:"name"`
		ExpectCode string `json:"expect_code"`
		Line       string `json:"line"`
	} `json:"undeclared_methods"`
	Responses []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Line string `json:"line"`
	} `json:"responses"`
}

func loadFixtures(t *testing.T) frameFixtures {
	t.Helper()
	data, err := os.ReadFile("../../testdata/control/frames.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures frameFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if fixtures.RPCVersion != Version {
		t.Fatalf("fixtures are for protocol v%d, code is v%d",
			fixtures.RPCVersion, Version)
	}
	return fixtures
}

// The fixtures are the shared reference for the Go side and the Lua bridge.
// If the decoder and the file ever disagree, one of them is lying to the other
// implementation about what the wire looks like.
func TestFixtureRequestsDecode(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures.ValidRequests) == 0 {
		t.Fatal("no valid request fixtures")
	}

	for _, fixture := range fixtures.ValidRequests {
		t.Run(fixture.Name, func(t *testing.T) {
			// Through the framing path, not just the decoder, so the fixture
			// proves a real frame works end to end.
			reader := bufio.NewReader(strings.NewReader(fixture.Line + "\n"))
			request, err := ReadRequest(reader, MaxRequestBytes)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if request.Method != fixture.Method {
				t.Fatalf("method = %q, want %q", request.Method, fixture.Method)
			}
			if _, declared := Lookup(request.Method); !declared {
				t.Fatalf("fixture uses %q, which is not in the catalogue", request.Method)
			}
			if strings.Contains(fixture.Line, "\n") {
				t.Fatal("fixture line contains a raw newline; frames are one line")
			}
		})
	}
}

func TestFixtureInvalidRequestsAreRejectedWithTheStatedCode(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures.InvalidRequests) == 0 {
		t.Fatal("no invalid request fixtures")
	}

	for _, fixture := range fixtures.InvalidRequests {
		t.Run(fixture.Name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(fixture.Line))
			if err == nil {
				t.Fatal("accepted")
			}
			code, ok := domain.CodeOf(err)
			if !ok || string(code) != fixture.ExpectCode {
				t.Fatalf("code = %q, want %q (err: %v)", code, fixture.ExpectCode, err)
			}
		})
	}
}

// A well-formed frame naming a method that does not exist is NotFound, and
// nothing about the name is ever executed.
func TestFixtureUndeclaredMethodsAreNotFound(t *testing.T) {
	fixtures := loadFixtures(t)
	registry := NewRegistry()

	for _, fixture := range fixtures.UndeclaredMethods {
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := DecodeRequest([]byte(fixture.Line))
			if err != nil {
				t.Fatalf("the frame itself should be well formed: %v", err)
			}
			response := registry.Dispatch(context.Background(), request)
			if response.OK {
				t.Fatal("an undeclared method was served")
			}
			if response.Error.Code != fixture.ExpectCode {
				t.Fatalf("code = %q, want %q", response.Error.Code, fixture.ExpectCode)
			}
		})
	}
}

func TestFixtureResponsesParseAndMatchTheEnvelope(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures.Responses) == 0 {
		t.Fatal("no response fixtures")
	}

	for _, fixture := range fixtures.Responses {
		t.Run(fixture.Name, func(t *testing.T) {
			var response Response
			if err := json.Unmarshal([]byte(fixture.Line), &response); err != nil {
				t.Fatalf("does not parse: %v", err)
			}
			if response.RPCVersion != Version {
				t.Fatalf("rpc_version = %d", response.RPCVersion)
			}
			if response.OK != fixture.OK {
				t.Fatalf("ok = %v, want %v", response.OK, fixture.OK)
			}
			if fixture.OK {
				if response.Error != nil {
					t.Fatal("a success carries an error")
				}
				return
			}
			if response.Error == nil {
				t.Fatal("a failure carries no error")
			}
			if string(response.Result) != "null" {
				t.Fatalf("result = %s, want null on a failure", response.Result)
			}
			// Retryability in the fixture must match what the code decides, or
			// a client written against the file would retry the wrong things.
			want := Retryable(domain.ErrorCode(response.Error.Code))
			if response.Error.Retryable != want {
				t.Fatalf("retryable = %v for %s, code says %v",
					response.Error.Retryable, response.Error.Code, want)
			}
			for key := range response.Error.Details {
				if !isAllowedDetailKey(key) {
					t.Fatalf("detail key %q is not on the whitelist", key)
				}
			}
		})
	}
}

func isAllowedDetailKey(key string) bool {
	for _, allowed := range AllowedDetailKeys() {
		if allowed == key {
			return true
		}
	}
	return false
}
