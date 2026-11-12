//go:build !windows

package atomicfile

import "os"

// syncDir makes the rename itself survive a power cut. The new file is
// already whole and in place by then, so a failure here is not reported:
// at worst the old file comes back, which is no worse than not saving,
// and some filesystems refuse to sync a directory at all.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
