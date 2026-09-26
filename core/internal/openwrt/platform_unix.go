//go:build unix

package openwrt

import (
	"io/fs"
	"os"
	"os/exec"
	"syscall"
)

// executable reports whether a path can be run.
//
// The execute bit is checked so that a directory or a data file sharing a name
// with a tool is treated as the tool being absent, which is the honest answer,
// rather than as an execution failure at the moment the feature is used.
func executable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}

// setProcessGroup puts the child in a process group of its own.
//
// `wifi reload` and the package managers start helpers. Killing only the
// process named on the command line leaves those helpers running, still holding
// the pipe this program is reading, so the timeout that was supposed to bound
// the call does not.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup kills everything the tool started, not just the tool.
//
// The negative pid is the group. If the group is already gone the signal fails
// and there is nothing left to do, which is why the error from the fallback is
// the one that matters.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
