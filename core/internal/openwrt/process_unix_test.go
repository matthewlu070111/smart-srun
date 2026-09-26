//go:build unix

package openwrt

import "syscall"

// processExists asks the kernel whether a pid is still alive. Signal 0 performs
// the permission and existence checks without delivering anything.
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
