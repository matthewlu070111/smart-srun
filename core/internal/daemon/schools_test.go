package daemon

import (
	"encoding/json"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/strategy"
)

// Every method the catalogue declares is either bound or deliberately absent.
//
// schools.list and schools.inspect sat in the catalogue unbound, so a client
// asking the daemon was told "not in this build" while the CLI answered the
// same question offline. The only name allowed to stay unbound is the one with
// no possible implementation yet.
func TestTheCatalogueIsBoundExceptWhatCannotRunYet(t *testing.T) {
	registry := control.NewRegistry()
	(&Daemon{}).register(registry)

	deliberatelyUnbound := map[string]bool{"school.command": true}
	for _, method := range control.Catalogue() {
		bound := registry.Registered(method.Name)
		switch {
		case deliberatelyUnbound[method.Name] && bound:
			t.Errorf("%s is bound; drop it from the deliberately-unbound list", method.Name)
		case !deliberatelyUnbound[method.Name] && !bound:
			t.Errorf("%s is catalogued but unbound; a client would be told it is not in this build", method.Name)
		}
	}
}

func TestSchoolsAreAnsweredFromTheSharedRegistry(t *testing.T) {
	registry := strategy.NewRegistry()
	registry.MustRegister(strategy.Default())
	registry.MustRegister(strategy.Strategy{ID: "rpc-visible", Label: "可见"})
	config.UseSchoolRegistry(registry, t.Cleanup)

	d := &Daemon{}
	listed, err := d.schoolsList(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.([]strategy.Strategy); len(got) != 2 || got[1].ID != "rpc-visible" {
		t.Errorf("schools.list = %+v", got)
	}

	raw, _ := json.Marshal(SchoolInspectParams{ID: "rpc-visible"})
	found, err := d.schoolsInspect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if found.(strategy.Strategy).Label != "可见" {
		t.Errorf("schools.inspect = %+v", found)
	}

	raw, _ = json.Marshal(SchoolInspectParams{ID: "nobody"})
	_, err = d.schoolsInspect(t.Context(), raw)
	if code, _ := domain.CodeOf(err); code != domain.CodeNotFound {
		t.Errorf("unknown strategy: code = %q, want NotFound", code)
	}
}
