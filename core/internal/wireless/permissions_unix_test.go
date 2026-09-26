//go:build unix

package wireless

import (
	"os"
	"testing"
)

// Both files are private, and the backup especially.
//
// The backup holds a wireless passphrase in clear. It lives in the runtime
// directory, which is already 0700, so this is the second of two defences --
// worth having because the first depends on a mode an installer or a restored
// backup can change, and this one does not.
//
// Asserted against the literal rather than against the constant: comparing
// info.Mode().Perm() to FileMode passes just as happily when somebody changes
// FileMode to 0644, which is the mistake this is here to catch. The constants
// are checked against the literals separately, below.
func TestTheJournalAndBackupArePrivate(t *testing.T) {
	where := paths(t)
	applied(t, homeStore(), where)

	for name, path := range map[string]string{
		"journal": where.journal(),
		"backup":  where.backup(),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", name, got)
		}
	}
}

func TestThePrivateModesAreTheDocumentedOnes(t *testing.T) {
	if FileMode != 0o600 {
		t.Errorf("FileMode = %04o, want 0600", FileMode)
	}
	if DirMode != 0o700 {
		t.Errorf("DirMode = %04o, want 0700", DirMode)
	}
}
