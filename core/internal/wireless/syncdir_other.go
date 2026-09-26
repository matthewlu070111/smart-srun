//go:build !unix

package wireless

// syncDir does nothing off the target platform.
//
// Windows has no fsync for a directory handle, and the daemon only runs on
// Linux. The rename is still atomic here, so a developer running these tests
// sees the same before-and-after states; what is missing is durability across
// power loss, which a development machine is not being asked for.
func syncDir(string) error { return nil }
