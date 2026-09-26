package config

import (
	"errors"
	"io/fs"
	"sync"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Change is one edit inside a configuration transaction.
//
// It receives a private copy, so a change that returns an error has altered
// nothing. Every writer -- the settings page, an account dialog, the CLI, the
// wizard -- goes through one of these rather than reading the configuration and
// writing a whole file back, which is how two concurrent edits lose one of
// themselves.
type Change func(*domain.Config) error

// Repository owns the configuration file.
//
// It is the single writer named in spec 02's ownership table. The in-memory
// snapshot is published only after the disk commit succeeds, so a reader can
// never observe a configuration that is not on disk.
type Repository struct {
	path string

	mu        sync.Mutex
	current   domain.Config
	persisted bool

	// hooks is nil outside tests; see writeHooks.
	hooks *writeHooks
}

// Open loads the configuration at path, or starts from defaults if there is
// none yet.
//
// A file that exists but cannot be loaded is an error, and the file is left
// untouched: a 1.x configuration or a corrupted one still holds the settings
// the user would otherwise have to reconstruct from memory. Starting from
// defaults in that situation would destroy them on the first save.
func Open(path string) (*Repository, error) {
	repository := &Repository{path: path}

	cfg, err := LoadFile(path)
	switch {
	case err == nil:
		repository.current = cfg
		repository.persisted = true
	case errors.Is(err, fs.ErrNotExist):
		repository.current = Normalize(Defaults())
		repository.persisted = false
	default:
		return nil, err
	}
	return repository, nil
}

// Path is the file this repository owns.
func (r *Repository) Path() string { return r.path }

// Persisted reports whether a configuration file has been read or written yet.
// A false here means the snapshot is the built-in defaults, which the UI shows
// differently from a configuration the user has actually saved.
func (r *Repository) Persisted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persisted
}

// Revision is the version a writer must present to change anything.
func (r *Repository) Revision() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current.Revision
}

// Snapshot returns an independent copy of the current configuration.
//
// A copy, not the stored value: Config holds slices, a map and a pointer, so
// handing out the stored one would let any reader edit the repository's state
// without going through a transaction, and without bumping the revision anyone
// else is checking against.
func (r *Repository) Snapshot() domain.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return CloneConfig(r.current)
}

// Update applies one change under compare-and-set.
//
// expectedRevision is what the caller last read. If the configuration has moved
// on since, the change is refused and the caller re-reads: this is the only
// thing that stops a settings page that was open for ten minutes from undoing
// an account added five minutes ago.
//
// The order is deliberate. Validation happens before anything touches the disk,
// and the in-memory snapshot is replaced only after the commit succeeds, so a
// failed save leaves both the file and the running daemon on the previous
// configuration.
func (r *Repository) Update(expectedRevision uint64, change Change) (domain.Config, error) {
	if change == nil {
		return domain.Config{}, domain.Errorf(domain.CodeInvalidArgument,
			"没有要应用的更改")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if expectedRevision != r.current.Revision {
		return domain.Config{}, domain.FieldErrorf(domain.CodeConflict, "revision",
			"配置已被其他地方修改（期望版本 %d，当前 %d），请重新加载后再保存",
			expectedRevision, r.current.Revision)
	}

	if r.current.Revision == ^uint64(0) {
		return domain.Config{}, domain.Errorf(domain.CodeConflict, "配置版本已达上限，无法继续保存")
	}
	next := CloneConfig(r.current)
	if err := change(&next); err != nil {
		return domain.Config{}, err
	}

	next.SchemaVersion = domain.ConfigSchemaVersion
	next = Normalize(next)
	RepairSelection(&next)
	next.Revision = r.current.Revision + 1

	if err := Validate(next); err != nil {
		return domain.Config{}, err
	}

	data, err := Marshal(next)
	if err != nil {
		return domain.Config{}, err
	}
	state, err := writeAtomic(r.path, data, r.hooks)
	if state.visible() {
		// The file now holds `next`, so the repository holds `next` -- whatever
		// else went wrong. Keeping the old value here on an error was the split:
		// the running daemon served a configuration that no longer existed on
		// disk, a restart read a different one, and the next compare-and-swap
		// judged against a revision the file had already moved past, so the
		// save after this one could silently overwrite it.
		//
		// Cloned again on the way in: the change function was handed a pointer
		// to `next`, and assigning the struct would leave the caller's slices
		// and the stored ones sharing a backing array, so a change that kept
		// its argument could still edit the stored configuration afterwards
		// with no transaction and no revision bump.
		r.current = CloneConfig(next)
		r.persisted = true
	}
	if err != nil {
		// Reported as a failure even when the bytes landed. The caller asked
		// for a durable save and did not get one; what it must not be told is
		// that nothing happened.
		return domain.Config{}, err
	}
	return CloneConfig(next), nil
}

// CloneConfig deep-copies a configuration.
//
// The pointer inside LoginShape is the one that bites: copying the struct
// copies the pointer, and a caller that then wrote through it would change the
// stored account's double_stack without a transaction and without a revision
// bump.
func CloneConfig(cfg domain.Config) domain.Config {
	clone := cfg

	if cfg.CampusAccounts != nil {
		clone.CampusAccounts = make([]domain.CampusAccount, len(cfg.CampusAccounts))
		for i, account := range cfg.CampusAccounts {
			clone.CampusAccounts[i] = cloneCampusAccount(account)
		}
	}
	if cfg.HotspotProfiles != nil {
		clone.HotspotProfiles = make([]domain.HotspotProfile, len(cfg.HotspotProfiles))
		copy(clone.HotspotProfiles, cfg.HotspotProfiles)
	}
	if cfg.SchoolExtra != nil {
		clone.SchoolExtra = make(map[string]any, len(cfg.SchoolExtra))
		for key, value := range cfg.SchoolExtra {
			clone.SchoolExtra[key] = cloneExtraValue(value)
		}
	}
	return clone
}

func cloneCampusAccount(account domain.CampusAccount) domain.CampusAccount {
	if account.Login.DoubleStack != nil {
		value := *account.Login.DoubleStack
		account.Login.DoubleStack = &value
	}
	return account
}

// cloneExtraValue copies the one composite shape school_extra accepts. Scalars
// are values already; a slice would otherwise stay shared with the caller.
func cloneExtraValue(value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	clone := make([]any, len(items))
	copy(clone, items)
	return clone
}
