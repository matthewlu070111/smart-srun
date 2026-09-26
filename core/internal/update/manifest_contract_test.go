package update

import (
	"encoding/json"
	"os"
	"testing"
)

// M21: the documentation site's check-main-contract.mjs reads this same
// producer fixture. These synthetic packages are never release/download data.
func TestManifestV1ConsumerContract(t *testing.T) {
	data, err := os.ReadFile("testdata/manifest-v1-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string `json:"name"`
		Accept bool   `json:"accept"`
		JSON   string `json:"json"`
	}
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) < 5 {
		t.Fatalf("invalid contract fixture: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.JSON))
			if (err == nil) != tc.Accept {
				t.Fatalf("accept=%v, error=%v", tc.Accept, err)
			}
		})
	}
}
