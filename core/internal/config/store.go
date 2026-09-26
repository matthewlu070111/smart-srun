package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Where the configuration lives, and the modes spec 02 fixes for it.
//
// The file holds campus passwords and wireless keys, so it is 0600 inside a
// 0700 directory: on a router every process that matters runs as root, and the
// directory is what keeps the file out of reach of anything that does not.
const (
	DefaultDir  = "/etc/smart-srun"
	DefaultPath = DefaultDir + "/config.json"

	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

// tempPattern keeps the temporary file in the same directory as the target so
// the rename is a rename and not a copy across filesystems. The leading dot
// keeps a half-written file from showing up in a casual listing.
const tempPattern = ".config-*.tmp"

// Parse is the one way a configuration document becomes a usable Config:
// decode strictly, canonicalise, then validate.
//
// One function rather than three calls at each site, because a caller that
// forgets Normalize gets a Config that fails validation for reasons the user
// never caused, and a caller that forgets Validate gets one the daemon cannot
// act on.
func Parse(data []byte) (domain.Config, error) {
	cfg, err := Decode(data)
	if err != nil {
		return domain.Config{}, err
	}
	cfg = Normalize(cfg)
	if err := Validate(cfg); err != nil {
		return domain.Config{}, err
	}
	return cfg, nil
}

// Marshal renders a configuration for disk.
//
// Indented because a person reads this file when something has gone wrong, and
// deterministic because encoding/json writes struct fields in declaration order
// and map keys sorted -- two saves of the same configuration produce the same
// bytes, so a diff means something changed.
func Marshal(cfg domain.Config) ([]byte, error) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidConfig,
			"配置无法序列化").Wrap(err)
	}
	return append(data, '\n'), nil
}

// LoadFile reads and prepares the configuration at path.
//
// A missing file is reported as such, distinguishably, because "not configured
// yet" and "configured wrongly" need different answers. Nothing here writes:
// a file that fails to load is left exactly as it is, so the user can still
// read the settings they are about to re-enter.
func LoadFile(path string) (domain.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Config{}, err
		}
		return domain.Config{}, domain.Errorf(domain.CodeInvalidConfig,
			"无法读取配置文件 %s", path).Wrap(err)
	}
	return Parse(data)
}

// writeHooks lets the tests fail each step of the commit sequence.
//
// Unexported and nil in production. The alternative -- an interface over the
// filesystem -- would put an indirection in the real write path to serve tests
// only, and the thing that has to be verified here is precisely that the real
// syscalls happen in the right order.
type writeHooks struct {
	afterWrite   func() error
	afterSync    func() error
	beforeRename func() error
	afterRename  func() error
}

func call(hook func() error) error {
	if hook == nil {
		return nil
	}
	return hook()
}

// writeAtomic replaces path's contents in one step.
//
// The sequence is fixed by spec 02 and every part of it is load-bearing:
//
//	temp file in the same directory, 0600   never a world-readable window
//	write the whole document                 a partial document is never named
//	fsync the file                           the bytes reach the disk...
//	rename over the target                   ...before anything points at them
//	fsync the directory                      the rename itself survives power loss
//
// If any step fails the target is untouched and the temporary file is removed,
// so the previous configuration is still complete and readable. That is the
// property the fault-injection tests check: after a failure at any stage, the
// file on disk parses and is either wholly the old configuration or wholly the
// new one.
//
// The returned commit says how far it got, and the caller needs it. "The target
// is untouched" stops being true at the rename: a failure after that point
// leaves the new configuration visible to every reader while the error says the
// save failed. A caller that reads only the error keeps its old value in memory
// and disagrees with its own file.
// commit says how far a write got, because "it failed" is not one state.
//
// The three are genuinely different to the caller: nothing changed, the change
// is visible but its survival across a power cut is unconfirmed, or it is
// visible and durable. Collapsing the middle one into the first is what split
// the repository from its own file -- the user was told the save failed, the
// running daemon kept the old values, and a restart read the new ones.
type commit int

const (
	commitNone commit = iota
	commitVisible
	commitDurable
)

// visible reports that readers can already see the new bytes.
func (c commit) visible() bool { return c >= commitVisible }

func writeAtomic(path string, data []byte, hooks *writeHooks) (commit, error) {
	if hooks == nil {
		// Substituted rather than checked at each call site: reading
		// hooks.afterWrite to pass it along would dereference the nil first.
		hooks = &writeHooks{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig,
			"无法创建配置目录 %s", dir).Wrap(err)
	}

	temp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig,
			"无法在 %s 创建临时文件", dir).Wrap(err)
	}
	tempName := temp.Name()

	committed := false
	defer func() {
		if !committed {
			temp.Close()
			os.Remove(tempName)
		}
	}()

	// CreateTemp already uses 0600; setting it explicitly means the guarantee
	// does not depend on that staying true.
	if err := temp.Chmod(FileMode); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig,
			"无法设置配置文件权限").Wrap(err)
	}

	if _, err := temp.Write(data); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "写入配置失败").Wrap(err)
	}
	if err := call(hooks.afterWrite); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "写入配置失败").Wrap(err)
	}

	if err := temp.Sync(); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "配置未能写入磁盘").Wrap(err)
	}
	if err := call(hooks.afterSync); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "配置未能写入磁盘").Wrap(err)
	}
	if err := temp.Close(); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "配置未能写入磁盘").Wrap(err)
	}

	if err := call(hooks.beforeRename); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "配置提交失败").Wrap(err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return commitNone, domain.Errorf(domain.CodeInvalidConfig, "配置提交失败").Wrap(err)
	}
	committed = true

	// Past this line the new configuration is what every reader sees, and no
	// failure below takes that back. Reporting these as commitNone is what let
	// the repository keep serving a configuration its own file no longer held.
	if err := call(hooks.afterRename); err != nil {
		return commitVisible, domain.Errorf(domain.CodeInvalidConfig,
			"配置已写入，但未能确认提交完成").Wrap(err)
	}
	if err := syncDir(dir); err != nil {
		// The rename is visible; only its survival across a power cut is
		// unconfirmed. That is worth telling the user and is not a reason to
		// pretend the save did not happen.
		return commitVisible, err
	}
	return commitDurable, nil
}
