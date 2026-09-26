package openwrt

import "testing"

func TestExportPreservesSingleMemberListAndQuotedMultiline(t *testing.T) {
	c, err := ParseUCIExport("firewall", []byte("package firewall\nconfig zone 'cfg01dc81'\n list network 'wan'\n option name 'quote'\\''back\\slash\nlist network evil'\n"))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := c.Section("cfg01dc81")
	if !ok || !s.Anonymous {
		t.Fatal("lost anonymous name")
	}
	v, ok := s.Lookup("network")
	if !ok || !v.IsList || len(v.List) != 1 || v.List[0] != "wan" {
		t.Fatal("lost single-member list")
	}
	if s.Get("name") != "quote'back\\slash\nlist network evil" {
		t.Fatal("split quoted value")
	}
}

func TestExportRejectsIncompleteAndAmbiguousInput(t *testing.T) {
	for _, text := range []string{"", "package other", "package firewall\nconfig zone", "package firewall\nconfig zone x\nlist network wan\noption network lan", "package firewall\noption key value", "package firewall\nconfig zone x\nconfig zone x", "package firewall\nconfig zone x\nunknown something"} {
		if _, err := ParseUCIExport("firewall", []byte(text)); err == nil {
			t.Fatal("accepted invalid export")
		}
	}
}
