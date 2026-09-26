//go:build !unix

package openwrt

import (
	"io/fs"
	"os"
	"os/exec"
)

// The target is OpenWrt. These exist so the package still builds and its tests
// still run on a development workstation; the process-group guarantee is a Unix
// one and the tests that check it are skipped elsewhere.

// executable has no permission bits to consult here: Windows decides by
// extension, and os.Stat reports 0666 for every ordinary file, so requiring an
// execute bit would report every tool as missing.
func executable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fs.ErrPermission
	}
	return nil
}

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
