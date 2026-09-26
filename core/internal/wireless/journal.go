// Package wireless applies a wireless change as a transaction that can be
// undone.
//
// Spec 04 does not ask for a config write. It asks for planned -> backed_up ->
// applied -> awaiting_confirm -> committed, with rolling_back ->
// rolled_back/recovery_required beside it, a journal that survives a reboot,
// and a rollback that restores only what this transaction wrote. The reason is
// in the failure it prevents: the change that puts a router on a new network is
// the change that can take it off the network entirely, and when that happens
// nobody is logged in to fix it.
//
// The rule that shapes everything here is "restore only if the value is still
// ours". A rollback that wrote the old configuration back unconditionally would
// undo whatever somebody else did in between -- and somebody else includes the
// user, editing the home access point from LuCI while this was running.
package wireless

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Phase is the transaction state spec 04 fixes.
type Phase string

const (
	// PhasePlanned -- the change is decided and nothing has been touched.
	PhasePlanned Phase = "planned"
	// PhaseBackedUp -- the previous values are recorded and recoverable.
	PhaseBackedUp Phase = "backed_up"
	// PhaseApplied -- the new values are live.
	PhaseApplied Phase = "applied"
	// PhaseAwaitingConfirm -- live and waiting for somebody to say it worked.
	// A transaction that reaches its expiry here is rolled back.
	PhaseAwaitingConfirm Phase = "awaiting_confirm"
	// PhaseCommitted -- confirmed. The journal and the backup are deleted.
	PhaseCommitted Phase = "committed"

	// PhaseRollingBack -- undoing. A crash here is resumable, which is why it
	// is a recorded phase rather than a moment inside a function.
	PhaseRollingBack Phase = "rolling_back"
	// PhaseRolledBack -- everything this transaction wrote is back as it was.
	PhaseRolledBack Phase = "rolled_back"
	// PhaseRecoveryRequired -- something this transaction wrote has since been
	// changed by somebody else, so it was left alone. A person has to look.
	PhaseRecoveryRequired Phase = "recovery_required"
)

// Terminal reports that a phase needs no further work.
func (p Phase) Terminal() bool {
	switch p {
	case PhaseCommitted, PhaseRolledBack, PhaseRecoveryRequired:
		return true
	default:
		return false
	}
}

// Key names one UCI option, or a section itself.
//
// An empty Option means the section rather than something in it, whose value is
// its type: `wireless.jxnu_sta_radio1=wifi-iface`. That has to be expressible,
// because the client section this program manages may not exist yet and uci
// will not set an option in a section that is not there -- so creating it is
// part of the change, and anything that is part of the change has to be in the
// journal that undoes it.
type Key struct {
	Section string
	Option  string
}

// IsSection reports that this key names a section rather than an option in one.
func (k Key) IsSection() bool { return k.Option == "" }

// Entry is one option this transaction changed.
//
// Hashes rather than values, because one of these options is a wireless
// passphrase and the journal outlives the transaction. The values needed to
// undo the change live in the backup file, which is 0600 and is deleted on
// confirmation.
type Entry struct {
	Key Key `json:"key"`
	// BeforeHash is the value that was there, or "" when the option was absent.
	BeforeHash string `json:"before_hash"`
	// BeforePresent separates "the option was empty" from "the option was not
	// there". uci treats those differently -- setting an empty string does not
	// write the line at all -- so undoing them differs too.
	BeforePresent bool `json:"before_present"`
	// AfterHash is what this transaction wrote, or "" for a deletion.
	AfterHash    string `json:"after_hash"`
	AfterDeleted bool   `json:"after_deleted"`
	BeforeList   bool   `json:"before_list,omitempty"`
	AfterList    bool   `json:"after_list,omitempty"`
}

// Journal is the minimal record that survives a reboot.
//
// Minimal is a requirement, not a preference: it is written to flash on a
// device whose flash is the part most likely to fail, and every field in it has
// to earn its place by being something recovery cannot work without.
type Journal struct {
	Version int    `json:"version"`
	TaskID  string `json:"task_id"`
	Phase   Phase  `json:"phase"`
	Package string `json:"package"`
	// ConfigRevision is the configuration this change belongs to. Recovery
	// compares it: a journal from before a configuration change describes a
	// world that no longer exists.
	ConfigRevision uint64 `json:"config_revision"`
	// Salt makes the hashes below useless to anybody who reads this file.
	//
	// A bare SHA-256 of an eight-character wireless passphrase is not a hash,
	// it is the passphrase with an extra step. The salt is random per
	// transaction and the journal is 0600 in a 0700 directory, so this is the
	// third of three defences rather than the only one.
	Salt    string  `json:"salt"`
	Entries []Entry `json:"entries"`

	StartedAt time.Time `json:"started_at"`
	// ExpiresAt is when an unconfirmed change is assumed to have failed. Spec
	// 04 gives the wizard fifteen minutes; past it, recovery rolls back rather
	// than leaving a router on a network nobody confirmed reaching.
	ExpiresAt time.Time `json:"expires_at"`
}

// JournalVersion is bumped when the record's shape changes. A journal this
// build cannot read is left alone and reported, never guessed at.
const JournalVersion = 2

// FileMode and DirMode: the journal and the backup are both private. The
// backup holds a passphrase in clear, and the journal holds enough structure to
// say which sections this program touches.
const (
	FileMode = 0o600
	DirMode  = 0o700
)

// Hash is how a value is recorded and compared.
func (j *Journal) Hash(value string) string {
	sum := sha256.Sum256([]byte(j.Salt + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// newSalt returns a random salt, or fails loudly.
//
// A transaction that could not get randomness must not fall back to a constant:
// the whole point of the salt is that it is not predictable.
func newSalt() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", domain.Errorf(domain.CodeInternal,
			"无法生成随机盐值").Wrap(err)
	}
	return hex.EncodeToString(raw), nil
}

// Backup is the sensitive half: the values needed to undo the change.
//
// Separate from the journal because it is deleted as soon as the change is
// confirmed, while the journal's last state is worth keeping until then, and
// because this one holds a passphrase in clear and the journal does not.
type Backup struct {
	TaskID string `json:"task_id"`
	// Values is the previous value of each key that had one.
	Values map[string]string `json:"values"`
}

// Paths is where the two files live.
type Paths struct {
	Dir string
}

func (p Paths) journal() string { return filepath.Join(p.Dir, "wireless-journal.json") }
func (p Paths) backup() string  { return filepath.Join(p.Dir, "wireless-backup.json") }

// keyString is the map key for a Key, since JSON objects need string keys.
//
// A section key is its bare name, which is also how uci writes it. Section and
// option names cannot contain a dot, so the two forms cannot collide.
func keyString(key Key) string {
	if key.IsSection() {
		return key.Section
	}
	return key.Section + "." + key.Option
}

// writePrivate writes a file atomically, privately, and durably.
//
// config has its own version of this and they are deliberately not shared: that
// one has to report whether the rename became visible so the repository can
// agree with its own file, and this one has no such caller. What both must do
// is fsync, and for the same reason -- a journal that did not reach the flash
// is a journal that will not be there after the power cut it exists for.
func writePrivate(path string, document any) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法编码 %s",
			filepath.Base(path)).Wrap(err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法创建目录 %s", dir).Wrap(err)
	}

	temp, err := os.CreateTemp(dir, ".tmp-*")
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

	if err := temp.Chmod(FileMode); err != nil {
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

func readPrivate(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, into); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"%s 内容无法解析", filepath.Base(path)).Wrap(err)
	}
	return nil
}

// SaveJournal writes the record.
func (p Paths) SaveJournal(journal *Journal) error {
	return writePrivate(p.journal(), journal)
}

// LoadJournal reads the record, reporting whether there was one at all.
//
// "There is no journal" is the ordinary case and not an error: it is what every
// clean start looks like.
func (p Paths) LoadJournal() (*Journal, bool, error) {
	var journal Journal
	if err := readPrivate(p.journal(), &journal); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if journal.Version != JournalVersion && journal.Version != 1 {
		// Left alone rather than guessed at. A record this build does not
		// understand describes changes it cannot safely undo.
		return nil, true, domain.Errorf(domain.CodeRecoveryRequired,
			"无线事务日志版本为 %d，本版本只认识 %d；已保留原文件，需要人工处理",
			journal.Version, JournalVersion)
	}
	return &journal, true, nil
}

// SaveBackup writes the values needed to undo the change.
func (p Paths) SaveBackup(backup *Backup) error {
	return writePrivate(p.backup(), backup)
}

// LoadBackup reads them.
func (p Paths) LoadBackup() (*Backup, bool, error) {
	var backup Backup
	if err := readPrivate(p.backup(), &backup); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &backup, true, nil
}

// Clear removes both files.
//
// Called on confirmation, and spec 04 asks for it in as many words: the
// passphrase copy must not outlive the change it existed for.
func (p Paths) Clear() error {
	for _, path := range []string{p.backup(), p.journal()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return domain.Errorf(domain.CodeInternal,
				"无法删除 %s", filepath.Base(path)).Wrap(err)
		}
	}
	return nil
}
