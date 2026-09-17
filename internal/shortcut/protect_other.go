//go:build !windows

package shortcut

import (
	"errors"
	"os"
)

type platformProtector struct{}

func (platformProtector) protect([]byte) ([]byte, error) {
	return nil, errors.New("shortcut identity requires Windows DPAPI")
}

func (platformProtector) unprotect([]byte) ([]byte, error) {
	return nil, errors.New("shortcut identity requires Windows DPAPI")
}

func replaceIdentity(from, to string) error { return os.Rename(from, to) }
