package strategy

import (
	"slices"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func codeOf(t *testing.T, err error) domain.ErrorCode {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok {
		t.Fatalf("error carries no code: %v", err)
	}
	return code
}

// testStrategy is a school that needs settings and a verb of its own.
//
// It is the proof spec 07 asks for: a school can be added by declaring it, with
// no code written for that school anywhere. If this needed a Go file per
// school, the extension surface would not be doing its job.
func testStrategy() Strategy {
	return Strategy{
		ID:    "example-university",
		Label: "示例大学",
		Fields: []Field{
			{Key: "campus", Label: "校区", Kind: FieldSelect,
				Options: []Option{{Value: "north", Label: "北区"},
					{Value: "south", Label: "南区"}},
				Help: "选择所在校区，不同校区的认证网关不同"},
			{Key: "vpn_fallback", Label: "失败时改用 VPN", Kind: FieldBool},
			{Key: "portal_note", Label: "备注", Kind: FieldString},
		},
		Commands: []Command{
			{Name: "campus-switch", Summary: "在南北校区之间切换认证网关"},
		},
	}
}

// T09 -- a school with settings and a command of its own needs no code.
func TestASchoolCanBeAddedByDeclarationAlone(t *testing.T) {
	registry := Builtin()
	if err := registry.Register(testStrategy()); err != nil {
		t.Fatalf("a well-formed declaration was refused: %v", err)
	}

	found, ok := registry.Lookup("example-university")
	if !ok {
		t.Fatal("the strategy was not found after registration")
	}
	if len(found.Fields) != 3 || len(found.Commands) != 1 {
		t.Errorf("the declaration was not kept intact: %+v", found)
	}
	if !found.DeclaresField("campus") || found.DeclaresField("not_declared") {
		t.Error("field ownership is wrong")
	}
}

// T09 -- a strategy may not take over a core command.
//
// A school that could claim "login" would change what the core verb means on
// that school's routers only, so every instruction and every support answer
// becomes conditional on which school is configured.
func TestAStrategyCannotShadowAReservedCommand(t *testing.T) {
	for _, reserved := range ReservedCommands {
		strategy := testStrategy()
		strategy.Commands = []Command{{Name: reserved, Summary: "试图占用"}}

		err := Validate(strategy)
		if err == nil {
			t.Errorf("a strategy was allowed to claim %q", reserved)
			continue
		}
		if !strings.Contains(err.Error(), reserved) {
			t.Errorf("the message for %q does not name it: %v", reserved, err)
		}
	}
}

// And the refusal happens at registration, not when somebody runs the command.
//
// By the time a user selects a broken strategy, the person who can fix it is
// not the person looking at the error.
func TestABadDeclarationIsRefusedAtRegistration(t *testing.T) {
	cases := map[string]func(*Strategy){
		"no id":                 func(s *Strategy) { s.ID = "" },
		"an id with a space":    func(s *Strategy) { s.ID = "example university" },
		"an id with a slash":    func(s *Strategy) { s.ID = "example/university" },
		"no label":              func(s *Strategy) { s.Label = "" },
		"a field with no key":   func(s *Strategy) { s.Fields[0].Key = "" },
		"an unknown field kind": func(s *Strategy) { s.Fields[0].Kind = "colour" },
		"a select with no options": func(s *Strategy) {
			s.Fields[0].Options = nil
		},
		"options on a plain string": func(s *Strategy) {
			s.Fields[2].Options = []Option{{Value: "x", Label: "X"}}
		},
		"a duplicate field key": func(s *Strategy) {
			s.Fields[1].Key = s.Fields[0].Key
		},
		"a duplicate option": func(s *Strategy) {
			s.Fields[0].Options = append(s.Fields[0].Options,
				Option{Value: "north", Label: "又一个北区"})
		},
		"a command with no summary": func(s *Strategy) {
			s.Commands[0].Summary = ""
		},
		"a command with a space": func(s *Strategy) {
			s.Commands[0].Name = "campus switch"
		},
	}

	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			strategy := testStrategy()
			break_(&strategy)

			registry := NewRegistry()
			err := registry.Register(strategy)
			if err == nil {
				t.Fatal("a malformed declaration was accepted")
			}
			if code := codeOf(t, err); code != domain.CodeInvalidConfig {
				t.Errorf("code = %s, want InvalidConfig", code)
			}
			if _, ok := registry.Lookup(strategy.ID); ok {
				t.Error("the malformed strategy is discoverable anyway; it " +
					"would fail later, in front of somebody who cannot fix it")
			}
		})
	}
}

// Every problem in a declaration is reported at once, not one reload at a time.
func TestEveryProblemInADeclarationIsReportedTogether(t *testing.T) {
	strategy := Strategy{
		ID:    "bad id",
		Label: "",
		Fields: []Field{
			{Key: "", Kind: "nonsense"},
			{Key: "ok", Kind: FieldSelect},
		},
		Commands: []Command{{Name: "login", Summary: ""}},
	}

	err := Validate(strategy)
	if err == nil {
		t.Fatal("a thoroughly broken declaration was accepted")
	}
	var set *domain.Errors
	if !asErrors(err, &set) {
		t.Fatalf("err = %T, want a set of problems", err)
	}
	if len(set.Items) < 6 {
		t.Errorf("reported %d problems, expected every one of them: %v",
			len(set.Items), set.Fields())
	}
}

func asErrors(err error, target **domain.Errors) bool {
	set, ok := err.(*domain.Errors)
	if ok {
		*target = set
	}
	return ok
}

// Registering the same ID twice is a conflict, not a silent replacement.
func TestRegisteringTheSameIDTwiceIsRefused(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testStrategy()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := registry.Register(testStrategy())
	if err == nil {
		t.Fatal("a duplicate registration silently replaced the first")
	}
	if code := codeOf(t, err); code != domain.CodeConflict {
		t.Errorf("code = %s, want Conflict", code)
	}
}

// The built-in strategy is registrable and is the general case: no private
// fields, no extra commands. A school is a row in a catalogue, not a module.
func TestTheBuiltInStrategyIsTheGeneralCase(t *testing.T) {
	registry := Builtin()

	found, ok := registry.Lookup(DefaultID)
	if !ok {
		t.Fatal("the default strategy is not registered")
	}
	if len(found.Fields) != 0 || len(found.Commands) != 0 {
		t.Errorf("the general strategy declares school-private things: %+v", found)
	}
	if len(registry.List()) != 1 {
		t.Errorf("this build has %d strategies, expected only the default",
			len(registry.List()))
	}
}

// The listing order is registration order, so a picker does not reshuffle
// between runs.
//
// Order is a guarantee one comparison barely tests. Go randomises map
// iteration, but with a handful of entries what it randomises is a rotation of
// insertion order, and most of the possible rotations are insertion order: a
// listing built from the map survived the first version of this test about one
// run in thirty. That is rare enough to look like a passing test and common
// enough to turn up in a mutation sweep as a missing one. So the registry gets
// enough entries to leave that layout, every call is checked against the
// expected order rather than against the first call, and there are enough calls
// that surviving all of them is not something luck does.
func TestTheListingOrderIsStable(t *testing.T) {
	registry := Builtin()
	ids := []string{
		"aaa", "zzz", "mmm", "bbb", "yyy", "nnn",
		"ccc", "xxx", "ooo", "ddd", "www", "ppp",
	}
	for _, id := range ids {
		if err := registry.Register(Strategy{ID: id, Label: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	want := append([]string{DefaultID}, ids...)

	// The premise, checked rather than assumed: map order and registration
	// order have to actually differ here, or everything below would hold just
	// as well for a listing built from the map and would be testing nothing.
	// Iteration is re-randomised per range, so a few attempts settle it.
	disagrees := false
	for attempt := 0; attempt < 100 && !disagrees; attempt++ {
		viaMap := make([]string, 0, len(registry.items))
		for id := range registry.items {
			viaMap = append(viaMap, id)
		}
		disagrees = !slices.Equal(viaMap, want)
	}
	if !disagrees {
		t.Fatal("map iteration came out in registration order every time, so " +
			"this test cannot tell a stable listing from a map-random one")
	}

	for call := range 100 {
		var got []string
		for _, item := range registry.List() {
			got = append(got, item.ID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("call %d: order = %v, want registration order %v",
				call, got, want)
		}
	}
}

// A strategy that is not registered is not found, rather than falling back to
// the default. Silently substituting one school's parameters for another's is
// worse than saying the configuration names something unknown.
func TestAnUnknownStrategyIsNotSilentlySubstituted(t *testing.T) {
	registry := Builtin()
	if _, ok := registry.Lookup("no-such-school"); ok {
		t.Error("an unknown strategy resolved to something")
	}
	if _, ok := registry.Lookup(""); ok {
		t.Error("an empty strategy id resolved to something")
	}
}

// The reserved list covers every verb this build dispatches. A verb quietly
// dropped from it becomes claimable by a strategy.
//
// It is a superset of the sixteen CLAUDE.md fixes: "service" and "version" are
// commands the CLI answers and were missing here while cli.CoreCommands kept a
// second copy that included them.
func TestTheReservedListCoversEveryDispatchedVerb(t *testing.T) {
	documented := []string{
		"status", "login", "logout", "relogin", "daemon", "schools", "config",
		"switch", "log", "enable", "disable", "help", "man", "update",
		"presets", "detect",
	}
	for _, name := range documented {
		if !slices.Contains(ReservedCommands, name) {
			t.Errorf("documented verb %q is no longer reserved", name)
		}
	}
	for _, name := range []string{"service", "version"} {
		if !slices.Contains(ReservedCommands, name) {
			t.Errorf("dispatched verb %q is not reserved; a strategy could shadow it", name)
		}
	}

	seen := map[string]bool{}
	for _, name := range ReservedCommands {
		if seen[name] {
			t.Errorf("reserved verb %q is listed twice", name)
		}
		seen[name] = true
	}
}
