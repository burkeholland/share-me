package transfer

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func cleanupTemporaryFiles(directory, prefix string) {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("Inspect interrupted transfer directory %s: %v", directory, err)
		return
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		log.Printf("Skip interrupted transfer cleanup for non-directory or linked path %s", directory)
		return
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		log.Printf("Read interrupted transfer directory %s: %v", directory, err)
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			log.Printf("Inspect interrupted transfer file %s: %v", entry.Name(), err)
			continue
		}
		if info.Mode().IsRegular() {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				log.Printf("Remove interrupted transfer file %s: %v", entry.Name(), err)
			}
		}
	}
}
