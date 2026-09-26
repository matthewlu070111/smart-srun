package config

import (
	"encoding/json"
	"testing"
)

func TestPresetUpdateDefaultsAndSettingsRoundTrip(t *testing.T) {
	cfg, err := Decode([]byte(`{"schema_version":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PresetUpdates.Enabled || cfg.PresetUpdates.Time.String() != "09:00" {
		t.Fatal(cfg.PresetUpdates)
	}
	for _, clock := range []string{"00:00", "23:59"} {
		cfg, err = Decode([]byte(`{"schema_version":2,"preset_updates":{"enabled":false,"time":"` + clock + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		settings := SettingsOf(cfg)
		copy := Defaults()
		if err := ApplySettings(settings)(&copy); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Decode(data)
		if err != nil || again.PresetUpdates.Enabled || again.PresetUpdates.Time.String() != clock {
			t.Fatal(again.PresetUpdates, err)
		}
	}
	for _, bad := range []string{`"24:00"`, `"9:00"`, `900`, `null`} {
		if _, err := Decode([]byte(`{"schema_version":2,"preset_updates":{"time":` + bad + `}}`)); err == nil {
			t.Fatalf("accepted invalid clock %s", bad)
		}
	}
}
