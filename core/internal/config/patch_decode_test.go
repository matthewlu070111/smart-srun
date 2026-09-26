package config

import (
	"strings"
	"testing"
)

// Exercise the JSON boundary before the repository transaction: a struct-only
// mutation test cannot catch omission, explicit null or credential aliases.
func TestJSONAccountEditsPreserveCredentialAndOverridePresence(t *testing.T) {
	repository := emptyRepository(t)
	for _, tc := range []struct {
		name, input, password string
		doubleStack           string
	}{
		{"create", `{"user_id":"student","wired_iface":"wan","password":"  original secret  ","login":{"double_stack":true}}`, "  original secret  ", "true"},
		{"omit", `{"id":"c1","label":"renamed","login":{"n":"201"}}`, "  original secret  ", "true"},
		{"false", `{"id":"c1","login":{"double_stack":false}}`, "  original secret  ", "false"},
		{"clear", `{"id":"c1","password":"","login":{"double_stack":null}}`, "", "unset"},
		{"literal placeholder", `{"id":"c1","password":"******","login":{"double_stack":true}}`, "******", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var patch CampusPatch
			if err := DecodePatch([]byte(tc.input), &patch, "login.double_stack"); err != nil {
				t.Fatal(err)
			}
			apply(t, repository, UpsertCampus(patch))
			persisted, err := LoadFile(repository.Path())
			if err != nil {
				t.Fatal(err)
			}
			account := persisted.CampusAccounts[0]
			state := "unset"
			if account.Login.DoubleStack != nil {
				state = "false"
				if *account.Login.DoubleStack {
					state = "true"
				}
			}
			if account.Password != tc.password || state != tc.doubleStack {
				t.Fatal("JSON edit changed credential or tri-state semantics after reload")
			}
		})
	}
}

func TestCredentialJSONRejectsAmbiguityAndKeepsErrorsRedacted(t *testing.T) {
	for _, input := range []string{
		`{"password":"private-value","password":"replacement"}`,
		`{"password":"private-value","pass\u0077ord":"replacement"}`,
		`{"password":"private-value","Password":"replacement"}`,
		`{"password":"private-value","login":{"Double_Stack":true}}`,
		`{"password":"private-value","login":{"n":"200","n":"201"}}`,
		`{"password":null}`, `{"login":null}`,
		`{"password":"private-value","auth_enabled":"false"}`,
		`{"password":"private-value","login":{"n":123}}`,
		`{"password":"private-value","login":{"unknown":true}}`,
		`{"password":"private-value"} {}`, `[]`, `null`, ``,
		`{"password":"` + strings.Repeat("x", MaxConfigBytes) + `"}`,
	} {
		var patch CampusPatch
		err := DecodePatch([]byte(input), &patch, "login.double_stack")
		if err == nil {
			t.Fatal("ambiguous or malformed credential edit was accepted")
		}
		if strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "replacement") || len(err.Error()) > 512 {
			t.Fatal("decoder diagnostic exposed input values")
		}
	}
}

func TestNullPermissionIsLimitedToTheExactNestedField(t *testing.T) {
	input := []byte(`{"login":{"double_stack":null}}`)
	for _, nullable := range [][]string{nil, {"double_stack"}, {"login"}} {
		var patch CampusPatch
		if err := DecodePatch(input, &patch, nullable...); err == nil {
			t.Fatal("null permission applied outside the requested path")
		}
	}
	var patch CampusPatch
	if err := DecodePatch(input, &patch, "login.double_stack"); err != nil {
		t.Fatal(err)
	}
	if patch.Login == nil || patch.Login.DoubleStack == nil || *patch.Login.DoubleStack != nil {
		t.Fatal("explicit null was confused with an omitted edit")
	}
}
