package wireless

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type packageStore struct {
	packages map[string]*fakeStore
	reloads  int
}

func (s *packageStore) Read(ctx context.Context, pkg string, keys []Key) (map[Key]Value, error) {
	return s.packages[pkg].Read(ctx, pkg, keys)
}
func (s *packageStore) SectionKeys(ctx context.Context, pkg, section string) ([]Key, error) {
	return s.packages[pkg].SectionKeys(ctx, pkg, section)
}
func (s *packageStore) Stage(ctx context.Context, pkg string, changes []Change) error {
	return s.packages[pkg].Stage(ctx, pkg, changes)
}
func (s *packageStore) Commit(ctx context.Context, pkg string) error {
	return s.packages[pkg].Commit(ctx, pkg)
}
func (s *packageStore) PendingChanges(ctx context.Context, pkg string) ([]string, error) {
	return s.packages[pkg].PendingChanges(ctx, pkg)
}
func (s *packageStore) Reload(context.Context) error { s.reloads++; return nil }

func groupFixture(t *testing.T) (*Group, *packageStore, Paths) {
	t.Helper()
	s := &packageStore{packages: map[string]*fakeStore{"wireless": homeStore(), "network": newStore(map[Key]Value{ssid: {Text: "old-iface", Present: true}}), "firewall": newStore(map[Key]Value{ssid: {Text: `["wan"]`, Present: true, IsList: true}})}}
	p := Paths{Dir: t.TempDir()}
	g, err := BeginGroup(t.Context(), s, p, GroupPlan{TaskID: "wizard", ConfigRevision: 7, ConfirmWithin: 15 * time.Minute, Packages: []PackagePlan{
		{Package: "network", Changes: []Change{{Key: ssid, Text: "new-iface"}}},
		{Package: "firewall", Changes: []Change{ListChange(ssid, "wan", "new-iface")}},
		{Package: "wireless", Changes: campusPlan().Changes},
	}}, fixedClock(epoch))
	if err != nil {
		t.Fatal(err)
	}
	return g, s, p
}

func TestGroupFailureAfterTwoCommitsRestoresAllPackages(t *testing.T) {
	g, s, _ := groupFixture(t)
	s.packages["wireless"].failStage = errors.New("injected")
	if err := g.Apply(t.Context()); err == nil {
		t.Fatal("accepted failed apply")
	}
	if s.reloads != 0 {
		t.Fatal("reloaded incomplete configuration")
	}
	if err := g.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s.packages["network"].values[ssid].Text != "old-iface" || s.packages["firewall"].values[ssid] != (Value{Text: `["wan"]`, Present: true, IsList: true}) {
		t.Fatal("lost original configuration or singleton list type")
	}
	if s.reloads != 1 {
		t.Fatal("rollback should reload exactly once")
	}
}

func TestGroupRecoversSavedAccountWithoutUndoingItsConnection(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "save-did-not-land", true: "save-landed"}[saved], func(t *testing.T) {
			g, s, p := groupFixture(t)
			if err := g.Apply(t.Context()); err != nil {
				t.Fatal(err)
			}
			if s.reloads != 1 {
				t.Fatal("multiple reloads while applying")
			}
			if err := g.PrepareSave(8, []byte("exact normalized config")); err != nil {
				t.Fatal(err)
			}
			g, _, err := LoadGroup(s, p, fixedClock(epoch))
			if err != nil {
				t.Fatal(err)
			}
			revision, config := uint64(7), []byte("old config")
			if saved {
				revision, config = 8, []byte("exact normalized config")
			}
			if err := g.Recover(t.Context(), revision, config); err != nil {
				t.Fatal(err)
			}
			want := "old-network"
			if saved {
				want = "jxnu_stu"
			}
			if s.packages["wireless"].values[ssid].Text != want {
				t.Fatal("wrong recovery decision")
			}
			if _, found, err := LoadGroup(s, p, nil); err != nil || found {
				t.Fatal("group retained", err)
			}
		})
	}
}

func TestGroupNeverTreatsAnUnrelatedSaveAsConfirmation(t *testing.T) {
	g, s, p := groupFixture(t)
	if err := g.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := g.PrepareSave(8, []byte("expected")); err != nil {
		t.Fatal(err)
	}
	if err := g.Recover(t.Context(), 8, []byte("other")); err == nil {
		t.Fatal("accepted wrong document")
	}
	if s.packages["wireless"].values[ssid].Text != "jxnu_stu" {
		t.Fatal("undid possibly saved configuration")
	}
	g, found, err := LoadGroup(s, p, nil)
	if err != nil || !found || g.Phase() != PhaseRecoveryRequired {
		t.Fatal("lost recovery evidence", err)
	}
	if err := g.Recover(t.Context(), 8, []byte("other")); err == nil {
		t.Fatal("cleared unresolved recovery")
	}
}

func TestGroupCommittedDecisionSurvivesPartialChildCleanup(t *testing.T) {
	g, s, p := groupFixture(t)
	if err := g.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	g.record.Phase = PhaseCommitted
	if err := g.save(); err != nil {
		t.Fatal(err)
	}
	tx, _, _ := g.transaction("network")
	if err := tx.Confirm(); err != nil {
		t.Fatal(err)
	}
	g, _, _ = LoadGroup(s, p, nil)
	if err := g.Recover(t.Context(), 8, nil); err != nil {
		t.Fatal(err)
	}
	if s.packages["network"].values[ssid].Text != "new-iface" {
		t.Fatal("rolled back durable commit decision")
	}
}

func TestListTypeAndThirdPartySectionEditsSurviveRollback(t *testing.T) {
	store := homeStore()
	where := paths(t)
	section := Key{Section: "owned"}
	option := Key{Section: "owned", Option: "network"}
	plan := Plan{TaskID: "t", Package: "wireless", ConfigRevision: 7, ConfirmWithin: time.Minute,
		Changes: []Change{{Key: section, Text: "wifi-iface"}, ListChange(option, "wwan")}}
	tx, err := Begin(t.Context(), store, where, plan, fixedClock(epoch))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Apply(t.Context(), plan.Changes); err != nil {
		t.Fatal(err)
	}
	foreign := Key{Section: "owned", Option: "third_party"}
	store.values[foreign] = Value{Text: "preserve", Present: true}
	if _, err := tx.Rollback(t.Context()); err == nil {
		t.Fatal("removed third-party section contents")
	}
	if !store.values[section].Present || store.values[foreign].Text != "preserve" {
		t.Fatal("lost third-party change")
	}
	if _, _, err := Recover(t.Context(), store, where, 7, nil); err == nil {
		t.Fatal("lost unresolved conflict on restart")
	}
	if _, found, _ := (Paths{Dir: filepath.Clean(where.Dir)}).LoadJournal(); !found {
		t.Fatal("erased recovery journal")
	}
}
