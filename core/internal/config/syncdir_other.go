//go:build !unix

package config

// syncDir does nothing off the target platform.
//
// Windows has no fsync for a directory handle, and the daemon only ever runs on
// Linux. The rename is still atomic here, so a developer running the tests
// locally sees the same before-and-after states; what is missing is durability
// across power loss, which is not a property a development machine is being
// asked for.
func syncDir(string) error { return nil }
