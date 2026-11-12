//go:build windows

package atomicfile

// syncDir does nothing: Windows cannot flush a directory the way Go opens
// one, so making the rename last is left to the filesystem.
func syncDir(string) {}
