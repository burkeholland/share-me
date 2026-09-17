//go:build !windows

package peer

import (
	"errors"
	"os"
)

type platformProtector struct{}

func (platformProtector) protect([]byte) ([]byte, error) {
	return nil, errors.New("persistent peer identity requires Windows DPAPI")
}

func (platformProtector) unprotect([]byte) ([]byte, error) {
	return nil, errors.New("persistent peer identity requires Windows DPAPI")
}

func replaceIdentity(from, to string) error {
	return os.Rename(from, to)
}
