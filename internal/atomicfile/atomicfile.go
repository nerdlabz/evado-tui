// Package atomicfile replaces a file in one step, so that neither a crash
// nor a power cut can leave half of one: the files evado keeps on this
// computer are refused when damaged, and some of them hold the only copy
// of todos not yet synced.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data, readable by its owner only, creating the
// directory if need be. The data goes to a temporary file beside path and
// reaches the disk before the rename puts it in place; otherwise a power
// cut could keep the rename and lose the data, leaving an empty file
// where a whole one was. A failed write leaves the old file as it was and
// no temporary file behind.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// A name of its own, so two copies of evado saving at once never write
	// into the same temporary file.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	// CreateTemp makes the file 0600 already; saying so again guards
	// against a platform that does otherwise.
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	syncDir(dir)
	return nil
}
