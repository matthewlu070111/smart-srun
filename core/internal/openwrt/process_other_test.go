//go:build !unix

package openwrt

// The test that uses this is skipped off Unix; this exists so the package still
// compiles on a development workstation.
func processExists(int) bool { return false }
