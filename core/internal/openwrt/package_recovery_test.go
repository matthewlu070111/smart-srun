//go:build unix

package openwrt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

func TestAPKRecoveryReinstallsFilesWhenDatabaseAlreadyHasOldVersion(t *testing.T) {
	for _, failAdd := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale database", true: "failed downgrade"}[failAdd], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			archive := filepath.Join(dir, "original.apk")
			binary := filepath.Join(dir, "binary")
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(archive, "original program")
			write(binary, "interrupted upgrade")
			addExit := "0"
			if failAdd {
				addExit = "77"
			}
			// The native simulator models the discovered failure: add sees the
			// original version in its database and leaves the new binary untouched.
			// Only fix restores it, reading the private cache link to the backup.
			program := `#!/bin/sh
dir=${0%/*}
case "$1" in adbdump)
  printf '%s' '{"info":{"name":"smart-srun","version":"2.0.0_rc1-r1","hashes":"0123456789abcdef0123456789abcdef01234567"}}'
  exit 0;;
esac
test "${SMARTSRUN_INSTALL_WORKER}" = 1 || exit 90
case " $* " in *' --network=no '*) ;; *) exit 91;; esac
case " $* " in *' add '*) exit ` + addExit + `;; esac
case " $* " in *' --repositories-file /dev/null '*) ;; *) exit 92;; esac
cache=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --cache-dir ]; then shift; cache=$1; fi
  if [ "$1" = fix ]; then break; fi
  shift
done
test "$*" = 'fix --reinstall smart-srun' || exit 93
test -L "$cache/smart-srun-2.0.0_rc1-r1.01234567.apk" || exit 94
/bin/cat "$cache/smart-srun-2.0.0_rc1-r1.01234567.apk" > "$dir/binary"
printf 'native cache metadata' > "$cache/installed"
`
			write(filepath.Join(dir, "apk"), program)
			device := PackageDevice{Runner: Runner{SearchPath: []string{dir}}, Capabilities: Capabilities{PackageManager: PackageManagerAPK}}
			files := []update.LocalPackage{{Path: archive, Asset: update.Asset{Kind: "core", PackageManager: "apk", PackageVersion: "2.0.0_rc1-r1"}}}
			err := device.Recover(files)
			if failAdd {
				if code, _ := domain.CodeOf(err); code != domain.CodeInstallFailed {
					t.Fatalf("failed add accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(binary)
			want := "original program"
			if failAdd {
				want = "interrupted upgrade"
			}
			if err != nil || string(got) != want {
				t.Fatalf("binary=%q %v", got, err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, "smart-srun-apk-recovery-*"))
			if len(leftovers) != 0 {
				t.Fatalf("cache not removed: %v", leftovers)
			}
			got, err = os.ReadFile(archive)
			if err != nil || string(got) != "original program" {
				t.Fatalf("backup changed: %q %v", got, err)
			}
		})
	}
}

func TestAPKRecoveryCacheRejectsInvalidIdentity(t *testing.T) {
	asset := update.Asset{Kind: "core", PackageVersion: "2.0.0_rc1-r1"}
	for _, hashes := range []string{"", "01234567", strings.Repeat("z", 40), strings.Repeat("AB", 20), strings.Repeat("aa", 21)} {
		data, _ := json.Marshal(map[string]any{"info": map[string]string{"name": "smart-srun", "version": asset.PackageVersion, "hashes": hashes}})
		if _, err := apkRecoveryCacheName(data, asset); err == nil {
			t.Fatalf("accepted hash %q", hashes)
		}
	}
	for _, version := range []string{"", "../escape", "bad\\path", "bad\nline"} {
		asset.PackageVersion = version
		data, _ := json.Marshal(map[string]any{"info": map[string]string{"name": "smart-srun", "version": version, "hashes": strings.Repeat("ab", 20)}})
		if _, err := apkRecoveryCacheName(data, asset); err == nil {
			t.Fatalf("accepted version %q", version)
		}
	}
}

func TestOpkgRecoveryForcesSameVersionReinstall(t *testing.T) {
	dir := t.TempDir()
	program := "#!/bin/sh\ntest \"$*\" = '--force-downgrade --force-reinstall install /tmp/old.ipk'\n"
	if err := os.WriteFile(filepath.Join(dir, "opkg"), []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	device := PackageDevice{Runner: Runner{SearchPath: []string{dir}}, Capabilities: Capabilities{PackageManager: PackageManagerOpkg}}
	if err := device.Recover([]update.LocalPackage{{Path: "/tmp/old.ipk", Asset: update.Asset{PackageManager: "opkg"}}}); err != nil {
		t.Fatal(err)
	}
}
