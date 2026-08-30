package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

// WriteAtomic creates a temporary sibling, writes and syncs it, sets
// permissions, renames it over the destination, and syncs the parent
// directory.
func WriteAtomic(path string, data []byte, mode fs.FileMode) error {
	pf, err := renameio.NewPendingFile(path, renameio.WithStaticPermissions(mode.Perm()))
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	defer pf.Cleanup()
	if _, err := pf.Write(data); err != nil {
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := pf.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("atomic replace %s: %w", path, err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent dir of %s: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync parent dir of %s: %w", path, err)
	}
	return nil
}
