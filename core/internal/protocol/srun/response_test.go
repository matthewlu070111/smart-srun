package srun

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// T04 -- what the parser accepts.
func TestParseJSONPAcceptsWhatGatewaysActuallySend(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"a JSONP call", `jQuery112404953340710317169_1758000000({"error":"ok"})`,
			`{"error":"ok"}`},
		{"a JSONP call terminated with a semicolon",
			`jQuery1124({"error":"ok"});`, `{"error":"ok"}`},
		{"trailing whitespace after the semicolon",
			"jQuery1124({\"error\":\"ok\"});\n\n", `{"error":"ok"}`},
		{"bare JSON, which some deployments return", `{"error":"ok"}`, `{"error":"ok"}`},
		{"bare JSON with surrounding whitespace", "\n  {\"error\":\"ok\"}  \n",
			`{"error":"ok"}`},
		{"a dotted callback", `window.cb({"error":"ok"})`, `{"error":"ok"}`},
		{"an underscore and dollar in the callback", `$_cb1({"error":"ok"})`,
			`{"error":"ok"}`},
		{"a nested object with brackets inside strings",
			`cb({"error":"ok","msg":"a(b)c","list":[1,2]})`,
			`{"error":"ok","msg":"a(b)c","list":[1,2]}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, kind, err := ParseJSONP([]byte(testCase.body), MaxAuthResponseBytes)
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if kind != KindObject {
				t.Fatalf("kind = %q, want object", kind)
			}
			if string(value) != testCase.want {
				t.Fatalf("payload = %s, want %s", value, testCase.want)
			}
			var probe map[string]any
			if err := json.Unmarshal(value, &probe); err != nil {
				t.Fatalf("the returned payload does not parse: %v", err)
			}
		})
	}
}

// T04 -- what it refuses, and how it labels each refusal.
func TestParseJSONPRefusals(t *testing.T) {
	cases := []struct {
		name string
		body string
		kind ResponseKind
		code domain.ErrorCode
	}{
		{"an empty body", "", KindEmpty, domain.CodeProtocolInvalid},
		{"whitespace only", "  \n\t ", KindEmpty, domain.CodeProtocolInvalid},
		{"a portal page", "<!DOCTYPE html>\n<html><body>请先登录</body></html>",
			KindHTML, domain.CodePortalHTMLResponse},
		{"a bare form", "<form action=\"/login\"></form>",
			KindHTML, domain.CodePortalHTMLResponse},
		{"an array", `[{"error":"ok"}]`, KindNonObject, domain.CodeProtocolInvalid},
		{"an array inside a callback", `cb([1,2,3])`, KindNonObject, domain.CodeProtocolInvalid},
		{"a bare string", `"ok"`, KindNonObject, domain.CodeProtocolInvalid},
		{"a bare null", `null`, KindNonObject, domain.CodeProtocolInvalid},
		{"a bare number", `42`, KindNonObject, domain.CodeProtocolInvalid},
		{"a truncated object", `cb({"error":"ok"`, KindMalformed, domain.CodeProtocolInvalid},
		{"a truncated call", `cb({"error":"ok"}`, KindMalformed, domain.CodeProtocolInvalid},
		{"not JSON at all", `totally not json`, KindMalformed, domain.CodeProtocolInvalid},
		{"two objects", `cb({"a":1}{"b":2})`, KindMalformed, domain.CodeProtocolInvalid},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, kind, err := ParseJSONP([]byte(testCase.body), MaxAuthResponseBytes)
			if err == nil {
				t.Fatalf("accepted, returning %s", value)
			}
			if value != nil {
				t.Fatalf("a refusal still returned a payload: %s", value)
			}
			if kind != testCase.kind {
				t.Errorf("kind = %q, want %q", kind, testCase.kind)
			}
			if code, _ := domain.CodeOf(err); code != testCase.code {
				t.Errorf("code = %q, want %q", code, testCase.code)
			}
		})
	}
}

// Code after the closing bracket must not be ignored. The baseline took
// everything between the first "(" and the last ")", so a response with
// something appended could still be read as a successful login.
func TestTrailingCodeIsNotIgnored(t *testing.T) {
	for _, body := range []string{
		`cb({"error":"ok"}) ; alert(1)`,
		`cb({"error":"ok"}) + more`,
		`cb({"error":"ok"});;`,
		`cb({"error":"ok"})junk`,
	} {
		if _, _, err := ParseJSONP([]byte(body), MaxAuthResponseBytes); err == nil {
			t.Errorf("accepted a response with trailing content: %s", body)
		}
	}
}

// The callback has to look like an identifier. Accepting any prefix means
// accepting a body that merely happens to contain a bracket.
func TestOnlyIdentifierShapedCallbacksAreUnwrapped(t *testing.T) {
	for _, body := range []string{
		`some text ({"error":"ok"})`,
		`<script>cb({"error":"ok"})`,
		`1cb({"error":"ok"})`,
		`({"error":"ok"})`,
		`cb cb({"error":"ok"})`,
		`../../cb({"error":"ok"})`,
	} {
		if _, _, err := ParseJSONP([]byte(body), MaxAuthResponseBytes); err == nil {
			t.Errorf("accepted %q as a JSONP wrapper", body)
		}
	}
}

// Nothing about the body may reach the error. A response can carry an account
// name, a session identifier or a whole portal page.
func TestRefusalsNeverQuoteTheBody(t *testing.T) {
	secrets := []string{"hunter2", "2020123456", "SESSIONID", "10.0.0.2"}
	bodies := []string{
		`cb({"password":"hunter2","user":"2020123456"}`,
		`<html><body>SESSIONID=hunter2 10.0.0.2</body></html>`,
		`hunter2 2020123456 SESSIONID 10.0.0.2`,
		`["hunter2","2020123456","SESSIONID","10.0.0.2"]`,
	}

	for _, body := range bodies {
		_, _, err := ParseJSONP([]byte(body), MaxAuthResponseBytes)
		if err == nil {
			t.Fatalf("accepted %q", body)
		}
		for _, secret := range secrets {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error %q leaked %q from the body", err, secret)
			}
		}
	}
}

// T04 -- the size cap. A gateway answering an authentication request with
// megabytes is serving a page, and reading it all first is how a bounded daemon
// stops being bounded.
func TestOversizedBodiesAreRefusedByTheirSize(t *testing.T) {
	body := append([]byte(`cb({"error":"ok","pad":"`),
		bytes.Repeat([]byte("x"), MaxAuthResponseBytes)...)
	body = append(body, []byte(`"})`)...)

	value, kind, err := ParseJSONP(body, MaxAuthResponseBytes)
	if err == nil {
		t.Fatal("an oversized body was parsed")
	}
	if kind != KindOversize {
		t.Fatalf("kind = %q, want oversize", kind)
	}
	if value != nil {
		t.Fatal("an oversized body still produced a payload")
	}

	// A limit of zero means no limit, for callers that have already bounded the
	// read themselves.
	if _, _, err := ParseJSONP([]byte(`{"error":"ok"}`), 0); err != nil {
		t.Fatalf("an unlimited parse failed: %v", err)
	}
}

// Charset conversion belongs to the transport. What this layer must not do is
// fail a working login because an error message arrived in another encoding, or
// hand back bytes it has quietly altered.
func TestANonUTF8ErrorMessageDoesNotFailTheWholeResponse(t *testing.T) {
	// "登录失败" in GBK, which is not valid UTF-8.
	gbk := []byte{0xb5, 0xc7, 0xc2, 0xbc, 0xca, 0xa7, 0xb0, 0xdc}
	body := append([]byte(`cb({"error":"ok","error_msg":"`), gbk...)
	body = append(body, []byte(`"})`)...)

	value, kind, err := ParseJSONP(body, MaxAuthResponseBytes)
	if err != nil {
		t.Fatalf("a response with a non-UTF-8 message was refused: %v", err)
	}
	if kind != KindObject {
		t.Fatalf("kind = %q", kind)
	}
	if !bytes.Contains(value, gbk) {
		t.Fatal("the raw bytes were altered; the caller can no longer convert them")
	}
	// The field that decides success is ASCII and still readable.
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(value, &probe); err != nil {
		t.Fatalf("the object does not decode: %v", err)
	}
	if probe.Error != "ok" {
		t.Fatalf("error = %q, want ok", probe.Error)
	}
}

// The kind is what gets logged, so every refusal must carry a distinct one and
// none of them may be the success value.
func TestEveryRefusalHasANonObjectKind(t *testing.T) {
	for _, body := range []string{"", "<html>", "[]", "nope", `cb({)`} {
		_, kind, err := ParseJSONP([]byte(body), MaxAuthResponseBytes)
		if err == nil {
			continue
		}
		if kind == KindObject || kind == "" {
			t.Errorf("body %q refused with kind %q", body, kind)
		}
	}
}
