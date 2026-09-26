//go:build linux

package integration

import (
	"os/exec"
)

// setLink brings a device up or down.
//
// Shelling out to ip rather than opening a netlink socket: this is test
// scaffolding for a topology that ip built in the first place, and the
// alternative is a hand-written RTM_SETLINK for the sake of avoiding one
// process in a test.
func setLink(device string, up bool) error {
	state := "down"
	if up {
		state = "up"
	}
	return exec.Command("ip", "link", "set", device, state).Run()
}
