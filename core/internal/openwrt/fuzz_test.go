package openwrt

import (
	"strings"
	"testing"
)

// FuzzParseUCIShow drives the tokenizer with arbitrary bytes.
//
// Its input is the output of a tool, and that output carries values a user
// typed: an SSID, a wireless key. The property is not that any particular
// document parses -- most of these will not -- but that the parser always
// either produces a configuration or reports a problem, and never panics,
// loops, or invents an option out of a value.
func FuzzParseUCIShow(f *testing.F) {
	f.Add("")
	f.Add("wireless.radio0=wifi-device\n")
	f.Add("wireless.s.ssid='Bob'\\''s Cafe'\n")
	f.Add("wireless.s.key='line1\nwireless.s.ssid='\\''HIJACKED'\\'''\n")
	f.Add("wireless.s.ssid='ends\\'\n")
	f.Add("wireless.s.list='a' 'b c' 'd'\\''e'\n")
	f.Add("wireless.s.ssid='unterminated\n")
	f.Add("wireless.s=\n")
	f.Add("wireless.=x\n")
	f.Add("=\n")
	f.Add("wireless.s.a='\\")

	f.Fuzz(func(t *testing.T, input string) {
		config, err := ParseUCIShow("wireless", []byte(input))
		if err != nil {
			return
		}

		for _, section := range config.Sections() {
			if section.Name == "" {
				t.Fatalf("parsed a section with no name from %q", input)
			}
			// Every option that was recorded must be reachable, and the order
			// must list exactly the options that exist -- a mismatch means a
			// value was filed under a name the caller cannot ask for.
			names := section.OptionNames()
			if len(names) != len(section.options) {
				t.Fatalf("section %q lists %d options but holds %d",
					section.Name, len(names), len(section.options))
			}
			for _, name := range names {
				if _, ok := section.Lookup(name); !ok {
					t.Fatalf("option %q is listed but not present", name)
				}
			}
			if _, ok := config.Section(section.Name); !ok {
				t.Fatalf("section %q is in the list but cannot be looked up",
					section.Name)
			}
		}

		// Nothing may be filed under a key that came from inside a value: every
		// section name has to appear in the input outside quotes. The cheap
		// version of that check is that it appears at all.
		for _, section := range config.Sections() {
			if !strings.Contains(input, section.Name) {
				t.Fatalf("section %q was invented; it is not in the input %q",
					section.Name, input)
			}
		}
	})
}

// FuzzParseUCIChanges drives the change reader the same way.
//
// An empty result is read as permission to modify the wireless configuration,
// so the property that matters is that output the reader cannot account for
// comes back as an error rather than as no changes.
func FuzzParseUCIChanges(f *testing.F) {
	f.Add("")
	f.Add("wireless.radio0.channel='11'\n")
	f.Add("-wireless.radio0.channel\n")
	f.Add("wireless.s.freq+='2412'\n")
	f.Add("wireless.s.ssid='two\nlines'\n")
	f.Add("-\n")
	f.Add("not a change\n")

	f.Fuzz(func(t *testing.T, input string) {
		changes, err := ParseUCIChanges([]byte(input))
		if err != nil {
			return
		}
		for _, change := range changes {
			if change.Key == "" {
				t.Fatalf("a change with no key came out of %q", input)
			}
			switch change.Kind {
			case UCIChangeSet, UCIChangeDelete, UCIChangeAppend:
			default:
				t.Fatalf("change kind %q from %q", change.Kind, input)
			}
			if change.Kind == UCIChangeDelete && change.Value != "" {
				t.Fatalf("a deletion carries a value: %+v", change)
			}
		}
		// Reporting no changes means the configuration is clean. Input that had
		// anything in it must not produce that answer silently.
		if len(changes) == 0 && strings.TrimSpace(input) != "" {
			t.Fatalf("%q was read as no pending changes at all", input)
		}
	})
}

// FuzzParseInterfaceStatus drives the ubus status reader.
//
// The property is that whatever comes back never yields an address to bind to
// unless it really was a usable IPv4 in the document.
func FuzzParseInterfaceStatus(f *testing.F) {
	f.Add(`{"up":true,"available":true,"l3_device":"eth0"}`)
	f.Add(`{"up":true,"ipv4-address":[{"address":"192.0.2.1","mask":24}]}`)
	f.Add(`{"ipv4-address":[{"address":"","mask":0}]}`)
	f.Add(`{"ipv4-address":[{"address":"::1","mask":128}]}`)
	f.Add(`{"l3_device":"a b"}`)
	f.Add(`{}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, input string) {
		status, err := ParseInterfaceStatus("wan", []byte(input))
		if err != nil {
			return
		}
		for _, address := range status.IPv4 {
			if !address.Address.Is4() {
				t.Fatalf("a non-IPv4 address survived: %v", address.Address)
			}
			if !strings.Contains(input, address.Address.String()) {
				t.Fatalf("address %v is not in the input", address.Address)
			}
		}
		for _, device := range []string{status.Device, status.L3Device} {
			if device == "" {
				continue
			}
			if normalized, ok := NormalizeDeviceName(device); !ok || normalized != device {
				t.Fatalf("device %q would not pass its own validation", device)
			}
		}
		if status.LinkState() == "" {
			t.Fatalf("no link state for %q", input)
		}
	})
}
