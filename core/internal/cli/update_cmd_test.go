package cli

import "testing"

func TestUpdateArgumentsCannotSupplyURLsOrArbitraryCommands(t *testing.T) {
	for _, args := range [][]string{
		{"run", "https://example.com/pkg.ipk"}, {"run", "--url", "https://example.com/pkg.ipk"},
		{"check", "--channel", "nightly"}, {"recover", "/tmp/arbitrary"}, {"status", "--background"},
		{"prepare-local", "--background"},
		{"inventory", "--background"}, {"inventory", "--channel", "rc"},
	} {
		if _, err := parseUpdateArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"check"}, {"check", "--channel", "rc", "--no-wait"}, {"recover", "--background"}, {"status", "--json"}, {"inventory", "--json"}, {"prepare-local"}} {
		if _, err := parseUpdateArgs(args); err != nil {
			t.Fatalf("rejected %v: %v", args, err)
		}
	}
}
