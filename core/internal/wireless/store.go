package wireless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

// commandRunner is the part of openwrt.Runner this store uses.
//
// Consumer-defined, like the adapter's own: it lets the composition -- which
// flags, in which order, and what is done with a failure -- be driven without a
// router, while the guarantees about running a process stay where they are
// tested against real ones.
type commandRunner interface {
	Run(ctx context.Context, program string, args ...string) (openwrt.Result, error)
	RunInput(ctx context.Context, program, input string, args ...string) (openwrt.Result, error)
}

// SystemConfigDir and SystemDeltaDir are uci's own defaults.
//
// Named and passed explicitly rather than left to uci, because the staging
// directory has to be passed explicitly anyway and a store that sometimes says
// where it is writing is a store that can be pointed somewhere unintended by
// forgetting a flag.
const (
	SystemConfigDir = "/etc/config"
	SystemDeltaDir  = "/tmp/.uci"
)

// networkInit is the service asked to make the new configuration live.
//
// `/etc/init.d/network reload` rather than `wifi reload`: the second restarts
// the radios and leaves netifd's view of the interfaces as it was, so a station
// that moved keeps the layer-3 configuration of the network it left. This
// project has been bitten by exactly that -- a stale route surviving a wireless
// change, and every subsequent request leaving through it.
const networkInit = "/etc/init.d/network"

// UCIStore is the Store over a router's real uci.
//
// It follows the baseline's sequence, which is the one that has run on real
// hardware: write the candidate into an isolated directory, check the live file
// has not moved underneath, then publish. The isolation is what makes a failure
// anywhere before the publish leave the running configuration untouched, and
// the check is what makes publishing a whole file equivalent to applying only
// this transaction's options.
//
// One consequence worth knowing before it surprises somebody: committing any
// change makes uci rewrite the whole package file in its own canonical form,
// which drops comments. Untouched sections, options and values survive
// exactly; only comments and blank-line layout do not. That is uci's, not this
// program's -- a `uci set` straight against /etc/config does the same, and so
// does LuCI -- but the file it happens to is the user's, so it is said here
// rather than discovered later. A package with no effective change is left
// alone entirely.
type UCIStore struct {
	runner commandRunner
	// staging is this store's own uci root: a copy of the package being
	// changed, plus a delta directory, neither of them shared with the system.
	staging string
	// configDir and deltaDir are the system's. Fields rather than constants so
	// the whole store can be pointed at a temporary tree and tested against a
	// real uci binary.
	configDir string
	deltaDir  string

	// staged is the live file as it was when the candidate was built, per
	// package. Commit compares against it, and its absence is how "commit
	// without stage" is refused rather than guessed at.
	staged map[string][]byte
}

// StoreOptions configures a UCIStore. Only Staging is required.
type StoreOptions struct {
	Staging   string
	ConfigDir string
	DeltaDir  string
}

// NewUCIStore builds a store. It creates nothing yet: a store that made
// directories at construction would leave them behind on a router that never
// changes its wireless configuration, which is most of them.
func NewUCIStore(runner commandRunner, options StoreOptions) (*UCIStore, error) {
	if runner == nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无线配置存储需要一个命令执行器")
	}
	if options.Staging == "" {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"无线配置存储需要一个独立的暂存目录")
	}
	store := &UCIStore{
		runner:    runner,
		staging:   options.Staging,
		configDir: options.ConfigDir,
		deltaDir:  options.DeltaDir,
		staged:    map[string][]byte{},
	}
	if store.configDir == "" {
		store.configDir = SystemConfigDir
	}
	if store.deltaDir == "" {
		store.deltaDir = SystemDeltaDir
	}
	return store, nil
}

// Read returns the current value of each key.
func (s *UCIStore) Read(ctx context.Context, pkg string, keys []Key) (map[Key]Value, error) {
	if err := checkName(pkg, "配置包"); err != nil {
		return nil, err
	}
	config, err := s.show(ctx, s.configDir, s.deltaDir, pkg)
	if err != nil {
		return nil, err
	}

	values := make(map[Key]Value, len(keys))
	for _, key := range keys {
		if err := checkKey(key); err != nil {
			return nil, err
		}
		section, ok := config.Section(key.Section)
		if !ok {
			values[key] = Value{}
			continue
		}
		if key.IsSection() {
			// A section's value is its type. Present means it exists, which is
			// what tells "create it" apart from "change what is in it" -- and
			// on the way back, "delete it" from "put its options back".
			values[key] = Value{Text: section.Type, Present: true}
			continue
		}
		option, present := section.Lookup(key.Option)
		if !present {
			values[key] = Value{}
			continue
		}
		if option.IsList {
			data, _ := json.Marshal(option.List)
			values[key] = Value{Text: string(data), Present: true, IsList: true}
			continue
		}
		if option.Text == "" {
			// uci has no empty option. Measured on a real device: a hand-written
			// `option blank ''` is not listed by show, not returned by get, and
			// `delete` on it answers "Entry not found". Reporting it as present
			// would put a value in the journal that no later uci call could
			// restore or remove, and the rollback would read the absence it
			// caused as somebody else's edit.
			values[key] = Value{}
			continue
		}
		values[key] = Value{Text: option.Text, Present: true}
	}
	return values, nil
}

func (s *UCIStore) SectionKeys(ctx context.Context, pkg, section string) ([]Key, error) {
	if err := checkName(pkg, "配置包"); err != nil {
		return nil, err
	}
	if err := checkName(section, "配置节"); err != nil {
		return nil, err
	}
	config, err := s.show(ctx, s.configDir, s.deltaDir, pkg)
	if err != nil {
		return nil, err
	}
	found, _ := config.Section(section)
	var keys []Key
	for _, option := range found.OptionNames() {
		keys = append(keys, Key{Section: section, Option: option})
	}
	return keys, nil
}

// Stage builds the candidate in the isolated directory.
//
// The live file is copied in and remembered, the options are written against
// the copy, and uci commits into the copy. Nothing the system reads has been
// touched when this returns.
func (s *UCIStore) Stage(ctx context.Context, pkg string, changes []Change) error {
	if err := checkName(pkg, "配置包"); err != nil {
		return err
	}
	delete(s.staged, pkg) // A failed replacement must not leave a publishable candidate.
	defer func() {
		if _, ready := s.staged[pkg]; !ready {
			_ = os.Remove(filepath.Join(s.staging, pkg))
		}
	}()
	if len(changes) == 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"没有要暂存的改动")
	}
	// Again here, not only in Begin: the rollback builds its own changes from
	// the backup and never passes through a plan.
	if err := checkWritable(changes); err != nil {
		return err
	}

	delta := filepath.Join(s.staging, "delta")
	if err := os.MkdirAll(delta, DirMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法创建暂存目录 %s", delta).Wrap(err)
	}
	// A delta left by an interrupted attempt would be replayed by the commit
	// below, applying options this transaction never planned.
	if err := clearDir(delta); err != nil {
		return err
	}

	live := filepath.Join(s.configDir, pkg)
	before, err := os.ReadFile(live)
	if err != nil {
		return domain.Errorf(domain.CodeNotFound,
			"无法读取 %s", live).Wrap(err)
	}
	if err := writeFilePrivate(filepath.Join(s.staging, pkg), before); err != nil {
		return err
	}

	// What is in the copy before anything is written to it, so a deletion of
	// something that is not there can be skipped rather than attempted.
	//
	// uci exits 1 for `delete` on a key it cannot find, with or without -q --
	// measured, not assumed. Treating a non-zero exit from delete as "probably
	// just missing" would swallow real failures, and attempting it anyway fails
	// the whole change: an open network's plan deletes `key`, and a section
	// that never had one is the ordinary case. Asking first is the version that
	// is both precise and correct.
	existing, err := s.existingKeys(ctx, delta, pkg)
	if err != nil {
		return err
	}
	unchanged, err := s.show(ctx, s.staging, delta, pkg)
	if err != nil {
		return err
	}
	if candidateMatches(unchanged, changes) {
		s.staged[pkg] = before
		return nil
	}

	var batch strings.Builder
	var keys []string
	for _, change := range ordered(changes) {
		if err := checkKey(change.Key); err != nil {
			return err
		}
		name := pkg + "." + keyString(change.Key)
		keys = append(keys, name)
		if change.Delete && !existing[keyString(change.Key)] {
			// Already the state this change asks for.
			continue
		}
		if change.IsList {
			if existing[keyString(change.Key)] {
				batch.WriteString("delete " + name + "\n")
			}
			items, _ := listItems(change.Text) // checkWritable validated the list.
			for _, item := range items {
				quoted, _ := quoteBatch(item)
				batch.WriteString("add_list " + name + "=" + quoted + "\n")
			}
		} else if change.Delete {
			batch.WriteString("delete " + name + "\n")
		} else {
			quoted, err := quoteBatch(change.Text)
			if err != nil {
				return err
			}
			batch.WriteString("set " + name + "=" + quoted + "\n")
		}
	}
	if batch.Len() > 0 {
		if _, err := s.runner.RunInput(ctx, "uci", batch.String(), "-q", "-c", s.staging, "-t", delta, "batch"); err != nil {
			return domain.Errorf(domain.CodeInternal, "无法暂存 UCI 改动（%s）", strings.Join(keys, ", ")).Wrap(err)
		}
	}

	if _, err := s.runner.Run(ctx, "uci", "-q", "-c", s.staging, "-t", delta,
		"commit", pkg); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法生成 %s 的候选配置", pkg).Wrap(err)
	}
	// Some UCI batch failures do not produce a failing exit status. Verify the
	// isolated candidate before marking it publishable, without exposing values.
	candidate, err := s.show(ctx, s.staging, delta, pkg)
	if err != nil {
		return err
	}
	if !candidateMatches(candidate, changes) {
		return domain.Errorf(domain.CodeInternal, "UCI 批处理未应用完整的候选配置")
	}
	s.staged[pkg] = before
	return nil
}

func candidateMatches(candidate openwrt.UCIConfig, changes []Change) bool {
	for _, change := range changes {
		section, present := candidate.Section(change.Key.Section)
		text := section.Type
		if !change.Key.IsSection() {
			var value openwrt.UCIValue
			value, present = section.Lookup(change.Key.Option)
			text = value.Text
			if value.IsList != change.IsList && !change.Delete {
				return false
			}
			if value.IsList {
				encoded, _ := json.Marshal(value.List)
				text = string(encoded)
			}
		}
		if change.Delete && present || !change.Delete && (!present || text != change.Text) {
			return false
		}
	}
	return true
}

func quoteBatch(text string) (string, error) {
	if strings.ContainsAny(text, "\x00\r\n") {
		return "", domain.Errorf(domain.CodeInvalidArgument, "UCI 值不能包含换行或空字符")
	}
	return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'", nil
}

// Commit publishes the candidate, if the live file is still the one it was
// built from.
//
// Refusing on a change underneath is spec 04's "校验原配置仍未变": somebody
// else's edit between the copy and the publish would be overwritten by a file
// that never contained it.
//
// The candidate is dropped when the publish succeeds, and only then. A Commit
// that published nothing leaves everything as Stage left it, so calling it
// again is a retry of the same operation rather than a second one -- and the
// alternative is a store that answers "nothing was staged" to a caller whose
// last staging is sitting right there, which describes the wrong problem.
//
// Half of that is covered by a test and half is not, so: the refusals above the
// publish are reachable and locked by TestACommitThatPublishedNothingCanBeRetried.
// replaceFile failing is not reachable from a test without giving this store a
// filesystem seam it otherwise has no use for, and a seam added for one test is
// production surface forever. It is written the same way because it is the same
// rule, not because the untested half was measured.
func (s *UCIStore) Commit(ctx context.Context, pkg string) error {
	if err := checkName(pkg, "配置包"); err != nil {
		return err
	}
	before, staged := s.staged[pkg]
	if !staged {
		// Not a device state a caller can recover from by retrying: it is this
		// program calling its own store out of order.
		return domain.Errorf(domain.CodeInternal,
			"%s 还没有候选配置可以应用", pkg)
	}

	live := filepath.Join(s.configDir, pkg)
	current, err := os.ReadFile(live)
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法读取 %s", live).Wrap(err)
	}
	if !bytes.Equal(current, before) {
		return domain.Errorf(domain.CodeConflict,
			"%s 在本次改动期间被其它操作修改，已放弃应用", pkg)
	}

	candidate, err := os.ReadFile(filepath.Join(s.staging, pkg))
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法读取 %s 的候选配置", pkg).Wrap(err)
	}
	if bytes.Equal(candidate, before) {
		// uci accepted every option and none of them changed anything. Writing
		// the identical file would still bump the mtime and make a later "was
		// this touched" answer wrongly.
		delete(s.staged, pkg)
		return os.Remove(filepath.Join(s.staging, pkg))
	}
	if err := replaceFile(live, candidate); err != nil {
		return err
	}
	delete(s.staged, pkg)
	return os.Remove(filepath.Join(s.staging, pkg))
}

// PendingChanges reports the system's own uncommitted changes.
//
// The system's delta, not this store's: the question is whether somebody else
// is mid-edit, and this transaction's own staging is by construction not.
func (s *UCIStore) PendingChanges(ctx context.Context, pkg string) ([]string, error) {
	if err := checkName(pkg, "配置包"); err != nil {
		return nil, err
	}
	result, err := s.runner.Run(ctx, "uci", "-c", s.configDir, "-t", s.deltaDir,
		"changes", pkg)
	if err != nil {
		return nil, missingPackage(pkg, err)
	}
	if result.StdoutTruncated {
		return nil, domain.Errorf(domain.CodeInternal,
			"%s 的未提交改动过多，无法完整读取", pkg)
	}
	changes, err := openwrt.ParseUCIChanges(result.Stdout)
	if err != nil {
		return nil, err
	}
	// Keys only. A pending change's value can be a passphrase somebody typed
	// into the wireless page, and this list exists to be shown in a refusal.
	names := make([]string, 0, len(changes))
	for _, change := range changes {
		names = append(names, string(change.Kind)+" "+change.Key)
	}
	return names, nil
}

// Reload makes the committed configuration take effect.
func (s *UCIStore) Reload(ctx context.Context) error {
	if _, err := s.runner.Run(ctx, networkInit, "reload"); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"重载网络配置失败").Wrap(err)
	}
	return nil
}

// existingKeys is every section and option the staged copy already holds.
//
// Read from the staging directory rather than the live one: they are identical
// at this point -- the copy was just made -- and asking the copy means this
// answer stays true for the commands that follow it even if the live file moves
// underneath. Commit checks that separately, and refuses.
func (s *UCIStore) existingKeys(ctx context.Context, delta, pkg string) (
	map[string]bool, error) {

	config, err := s.show(ctx, s.staging, delta, pkg)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, section := range config.Sections() {
		keys[section.Name] = true
		for _, option := range section.OptionNames() {
			keys[section.Name+"."+option] = true
		}
	}
	return keys, nil
}

func (s *UCIStore) show(ctx context.Context, configDir, deltaDir, pkg string) (
	openwrt.UCIConfig, error) {

	result, err := s.runner.Run(ctx, "uci", "-n", "-c", configDir, "-t", deltaDir,
		"export", pkg)
	if err != nil {
		return openwrt.UCIConfig{}, missingPackage(pkg, err)
	}
	if result.StdoutTruncated {
		return openwrt.UCIConfig{}, domain.Errorf(domain.CodeInternal,
			"配置 %s 过长，已截断", pkg)
	}
	return openwrt.ParseUCIExport(pkg, result.Stdout)
}

// missingPackage gives "there is no such configuration" its own code.
//
// A router with no radio has no /etc/config/wireless, and uci exits 1. That is
// a system with nothing to change, not a tool that failed.
func missingPackage(pkg string, err error) error {
	var exit *openwrt.ExitError
	if errors.As(err, &exit) && exit.Code == 1 {
		return domain.Errorf(domain.CodeNotFound,
			"系统上没有 %s 配置", pkg).Wrap(err)
	}
	return err
}

// writeFilePrivate writes the staging copy. It does not fsync: nothing outside
// this process reads it, and it is rebuilt from the live file every time.
func writeFilePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, FileMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法写入 %s", filepath.Base(path)).Wrap(err)
	}
	// WriteFile only applies the mode when it creates the file, and the copy
	// from a previous attempt is still there.
	if err := os.Chmod(path, FileMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法设置 %s 的权限", filepath.Base(path)).Wrap(err)
	}
	return nil
}

// replaceFile publishes the candidate over the live file, atomically and
// durably, keeping the mode the live file already had.
//
// Durably because this one is the change: a rename that reached the directory
// but not the flash would leave a router that reboots into the old wireless
// configuration while the journal says the new one is live and awaiting
// confirmation -- the one state the recovery cannot tell from a real one.
func replaceFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法读取 %s 的属性", path).Wrap(err)
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".smart-srun-*")
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法在 %s 创建临时文件", dir).Wrap(err)
	}
	name := temp.Name()
	committed := false
	defer func() {
		if !committed {
			temp.Close()
			os.Remove(name)
		}
	}()

	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法设置文件权限").Wrap(err)
	}
	if _, err := temp.Write(data); err != nil {
		return domain.Errorf(domain.CodeInternal, "写入失败").Wrap(err)
	}
	if err := temp.Sync(); err != nil {
		return domain.Errorf(domain.CodeInternal, "未能写入磁盘").Wrap(err)
	}
	if err := temp.Close(); err != nil {
		return domain.Errorf(domain.CodeInternal, "未能写入磁盘").Wrap(err)
	}
	if err := os.Rename(name, path); err != nil {
		return domain.Errorf(domain.CodeInternal, "提交失败").Wrap(err)
	}
	committed = true
	return syncDir(dir)
}

func checkName(name, what string) error {
	if !openwrt.IsLogicalInterfaceName(name) {
		return domain.Errorf(domain.CodeInvalidArgument,
			"%q 不是有效的%s名", name, what)
	}
	return nil
}

// checkKey refuses anything that would not survive being spliced into
// `package.section.option`.
//
// uci's own names allow letters, digits and underscores and nothing else, so a
// section called "radio0.key" is not a name uci could have produced -- it is a
// value that arrived from somewhere it should not have, and turning it into a
// different option than intended is exactly the failure worth refusing.
func checkKey(key Key) error {
	if err := checkName(key.Section, "配置节"); err != nil {
		return err
	}
	if key.IsSection() {
		// An empty option is the section itself, which is a name this store
		// writes on purpose rather than a name it failed to receive.
		return nil
	}
	return checkName(key.Option, "配置项")
}

// ordered puts uci's own sequencing rule where it belongs: in the store.
//
// A section has to exist before anything can be set in it, and has to still
// exist while its options are being removed. Callers building a plan get that
// right by accident or not at all, and the rollback builds its changes from a
// journal whose order is the plan's -- so a transaction that created a section
// would try to delete it first and then set options in what is no longer there.
//
// Three groups, stable within each: create the sections, change the options,
// then remove the sections.
func ordered(changes []Change) []Change {
	sorted := make([]Change, 0, len(changes))
	for _, change := range changes {
		if change.Key.IsSection() && !change.Delete {
			sorted = append(sorted, change)
		}
	}
	for _, change := range changes {
		if !change.Key.IsSection() {
			sorted = append(sorted, change)
		}
	}
	for _, change := range changes {
		if change.Key.IsSection() && change.Delete {
			sorted = append(sorted, change)
		}
	}
	return sorted
}

// clearDir removes a directory's entries without removing the directory, so
// its mode survives.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法读取暂存目录 %s", dir).Wrap(err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return domain.Errorf(domain.CodeInternal,
				"无法清理暂存目录 %s", dir).Wrap(err)
		}
	}
	return nil
}
