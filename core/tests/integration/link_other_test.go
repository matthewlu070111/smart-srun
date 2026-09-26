//go:build !linux

package integration

import "errors"

// The topology these tests need exists only on Linux; this keeps the package
// building on a development workstation, where every test in it skips anyway
// for want of the namespaces.
func setLink(string, bool) error {
	return errors.New("changing a device's state is a Linux operation")
}
