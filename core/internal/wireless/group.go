package wireless

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// PackagePlan names the exact options owned in one UCI package.
type PackagePlan struct {
	Package string
	Changes []Change
}

type GroupPlan struct {
	TaskID         string
	ConfigRevision uint64
	ConfirmWithin  time.Duration
	Packages       []PackagePlan
}

type groupRecord struct {
	Version        int       `json:"version"`
	TaskID         string    `json:"task_id"`
	Phase          Phase     `json:"phase"`
	Packages       []string  `json:"packages"`
	ConfigRevision uint64    `json:"config_revision"`
	ExpiresAt      time.Time `json:"expires_at"`
	Salt           string    `json:"salt"`
	SaveRevision   uint64    `json:"save_revision,omitempty"`
	SaveHash       string    `json:"save_hash,omitempty"`
}

// Group coordinates network, wireless and firewall as one recoverable change.
// Each package retains its own compare-before-restore journal. The group record
// precedes all children, so a crash between package commits remains recoverable.
// Callers serialize it with all other network changes.
type Group struct {
	store  Store
	paths  Paths
	now    func() time.Time
	record groupRecord
	plans  []PackagePlan
}

type deferredReload struct{ Store }

func (deferredReload) Reload(context.Context) error { return nil }
func (g *Group) path() string                       { return filepath.Join(g.paths.Dir, "group.json") }
func (g *Group) child(pkg string) Paths             { return Paths{Dir: filepath.Join(g.paths.Dir, pkg)} }
func (g *Group) save() error                        { return writePrivate(g.path(), &g.record) }
func (g *Group) Phase() Phase                       { return g.record.Phase }
func (g *Group) TaskID() string                     { return g.record.TaskID }
func (g *Group) ExpiresAt() time.Time               { return g.record.ExpiresAt }

func BeginGroup(ctx context.Context, store Store, paths Paths, plan GroupPlan, now func() time.Time) (*Group, error) {
	if now == nil {
		now = time.Now
	}
	if plan.TaskID == "" || len(plan.Packages) == 0 || len(plan.Packages) > 3 || plan.ConfirmWithin <= 0 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "无线组合事务计划无效")
	}
	if _, found, err := LoadGroup(store, paths, now); err != nil {
		return nil, err
	} else if found {
		return nil, domain.Errorf(domain.CodeConflict, "上次无线向导尚未收尾")
	}
	salt, err := newSalt()
	if err != nil {
		return nil, err
	}
	g := &Group{store: store, paths: paths, now: now, plans: plan.Packages,
		record: groupRecord{Version: 1, TaskID: plan.TaskID, Phase: PhasePlanned,
			ConfigRevision: plan.ConfigRevision, ExpiresAt: now().Add(plan.ConfirmWithin), Salt: salt}}
	seen := map[string]bool{}
	for _, part := range plan.Packages {
		if part.Package != "network" && part.Package != "wireless" && part.Package != "firewall" || seen[part.Package] || len(part.Changes) == 0 {
			return nil, domain.Errorf(domain.CodeInvalidArgument, "无线组合事务配置包无效")
		}
		if err := checkWritable(part.Changes); err != nil {
			return nil, err
		}
		seen[part.Package] = true
		g.record.Packages = append(g.record.Packages, part.Package)
	}
	if err := g.save(); err != nil {
		return nil, err
	}
	for _, part := range plan.Packages {
		_, err := Begin(ctx, deferredReload{store}, g.child(part.Package), Plan{
			TaskID: plan.TaskID, Package: part.Package, Changes: part.Changes,
			ConfigRevision: plan.ConfigRevision, ConfirmWithin: plan.ConfirmWithin}, now)
		if err != nil {
			undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
			defer cancel()
			return nil, errors.Join(err, g.Rollback(undo))
		}
	}
	g.record.Phase = PhaseBackedUp
	if err := g.save(); err != nil {
		return nil, err
	}
	return g, nil
}

func LoadGroup(store Store, paths Paths, now func() time.Time) (*Group, bool, error) {
	if now == nil {
		now = time.Now
	}
	g := &Group{store: store, paths: paths, now: now}
	if err := readPrivate(g.path(), &g.record); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	if g.record.Version != 1 || g.record.TaskID == "" || g.record.Salt == "" || len(g.record.Packages) == 0 || len(g.record.Packages) > 3 {
		return nil, true, domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务日志无效")
	}
	if !slices.Contains([]Phase{PhasePlanned, PhaseBackedUp, PhaseApplied, PhaseAwaitingConfirm, PhaseRollingBack, PhaseRolledBack, PhaseCommitted, PhaseRecoveryRequired}, g.record.Phase) {
		return nil, true, domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务阶段无法识别")
	}
	seen := map[string]bool{}
	for _, pkg := range g.record.Packages {
		if pkg != "network" && pkg != "wireless" && pkg != "firewall" || seen[pkg] {
			return nil, true, domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务配置包无效")
		}
		seen[pkg] = true
	}
	return g, true, nil
}

func (g *Group) transaction(pkg string) (*Transaction, bool, error) {
	j, found, err := g.child(pkg).LoadJournal()
	if err != nil || !found {
		return nil, found, err
	}
	if j.TaskID != g.record.TaskID || j.Package != pkg || j.ConfigRevision != g.record.ConfigRevision {
		return nil, true, domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务子记录不匹配")
	}
	return &Transaction{store: deferredReload{g.store}, paths: g.child(pkg), now: g.now, journal: j}, true, nil
}

func (g *Group) Apply(ctx context.Context) error {
	if g.record.Phase != PhaseBackedUp || len(g.plans) != len(g.record.Packages) {
		return domain.Errorf(domain.CodeConflict, "无线组合事务没有可应用的计划")
	}
	g.record.Phase = PhaseApplied
	if err := g.save(); err != nil {
		return err
	}
	for _, part := range g.plans {
		tx, found, err := g.transaction(part.Package)
		if err != nil {
			return err
		}
		if !found {
			return domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务缺少子记录")
		}
		if err := tx.Apply(ctx, part.Changes); err != nil {
			return err
		}
		if err := tx.AwaitConfirm(); err != nil {
			return err
		}
	}
	if err := g.store.Reload(ctx); err != nil {
		return err
	}
	g.record.Phase = PhaseAwaitingConfirm
	return g.save()
}

func (g *Group) configurationHash(data []byte) string {
	hash := sha256.New()
	hash.Write([]byte(g.record.Salt + "\x00"))
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

// PrepareSave records the exact next normalized config before the account is
// saved. Recovery may confirm only that revision AND document, never an unrelated
// later save. The record contains a salted digest, not account credentials.
func (g *Group) PrepareSave(revision uint64, config []byte) error {
	if g.record.Phase != PhaseAwaitingConfirm || revision != g.record.ConfigRevision+1 || len(config) == 0 {
		return domain.Errorf(domain.CodeConflict, "无线向导保存版本不匹配")
	}
	g.record.SaveRevision = revision
	g.record.SaveHash = g.configurationHash(config)
	return g.save()
}

// CheckApplied refuses to confirm options edited outside this transaction.
func (g *Group) CheckApplied(ctx context.Context) error {
	if g.record.Phase != PhaseAwaitingConfirm {
		return domain.Errorf(domain.CodeConflict, "无线组合事务尚未就绪")
	}
	for _, pkg := range g.record.Packages {
		tx, found, err := g.transaction(pkg)
		if err != nil {
			return err
		}
		if !found {
			return domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务缺少记录")
		}
		var keys []Key
		for _, entry := range tx.journal.Entries {
			keys = append(keys, entry.Key)
		}
		values, err := g.store.Read(ctx, pkg, keys)
		if err != nil {
			return err
		}
		for _, entry := range tx.journal.Entries {
			if !stillOurs(tx.journal, entry, values[entry.Key]) {
				return domain.Errorf(domain.CodeConflict, "无线配置已被其他操作修改，请取消向导后重新连接")
			}
		}
	}
	return nil
}

// Confirm records the durable group decision before removing any child's
// backup. Retrying after a crash completes that same decision.
func (g *Group) Confirm() error {
	if g.record.Phase != PhaseAwaitingConfirm && g.record.Phase != PhaseCommitted {
		return domain.Errorf(domain.CodeConflict, "无线组合事务尚不能确认")
	}
	g.record.Phase = PhaseCommitted
	if err := g.save(); err != nil {
		return err
	}
	for _, pkg := range g.record.Packages {
		tx, found, err := g.transaction(pkg)
		if err != nil {
			return err
		}
		if found {
			if err := tx.Confirm(); err != nil {
				return err
			}
		}
	}
	return g.clear()
}

func (g *Group) Rollback(ctx context.Context) error {
	if g.record.Phase == PhaseCommitted {
		return domain.Errorf(domain.CodeConflict, "已经确认的无线组合事务不能回滚")
	}
	if g.record.Phase == PhaseRecoveryRequired {
		return domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务仍需人工处理")
	}
	if g.record.Phase == PhaseApplied || g.record.Phase == PhaseAwaitingConfirm {
		for _, pkg := range g.record.Packages {
			_, found, err := g.transaction(pkg)
			if err != nil {
				return err
			}
			if !found {
				g.record.Phase = PhaseRecoveryRequired
				return errors.Join(domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务缺少恢复记录"), g.save())
			}
		}
	}
	wasApplied := g.record.Phase != PhasePlanned && g.record.Phase != PhaseBackedUp
	g.record.Phase = PhaseRollingBack
	if err := g.save(); err != nil {
		return err
	}
	var problems []error
	conflict := false
	for i := len(g.record.Packages) - 1; i >= 0; i-- {
		tx, found, err := g.transaction(g.record.Packages[i])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !found {
			continue
		} // A previous rollback already cleared this child.
		outcome, err := tx.Rollback(ctx)
		if outcome.Phase == PhaseRecoveryRequired {
			conflict = true
		}
		if err != nil {
			problems = append(problems, err)
		}
	}
	if wasApplied {
		if err := g.store.Reload(ctx); err != nil {
			problems = append(problems, err)
		}
	}
	if conflict {
		g.record.Phase = PhaseRecoveryRequired
		problems = append(problems, g.save(), domain.Errorf(domain.CodeRecoveryRequired, "无线组合事务有第三方改动，已保留恢复记录"))
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	g.record.Phase = PhaseRolledBack
	if err := g.save(); err != nil {
		return err
	}
	return g.clear()
}

func (g *Group) clear() error {
	if err := os.Remove(g.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(g.paths.Dir)
}

// Recover finishes a saved account's commit or rolls an abandoned wizard back.
// Browser progress is deliberately not resumed after a daemon restart.
func (g *Group) Recover(ctx context.Context, revision uint64, config []byte) error {
	if g.record.Phase == PhaseCommitted {
		return g.Confirm()
	}
	if g.record.Phase == PhaseRolledBack {
		return g.clear()
	}
	if g.record.Phase == PhaseRecoveryRequired {
		return domain.Errorf(domain.CodeRecoveryRequired, "无线向导仍需人工恢复")
	}
	if g.record.SaveRevision == revision && g.record.SaveHash != "" && g.record.SaveHash == g.configurationHash(config) {
		return g.Confirm()
	}
	if revision != g.record.ConfigRevision {
		g.record.Phase = PhaseRecoveryRequired
		return errors.Join(domain.Errorf(domain.CodeRecoveryRequired, "配置版本与无线向导不符，已保留恢复记录"), g.save())
	}
	return g.Rollback(ctx)
}
