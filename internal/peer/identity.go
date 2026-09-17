package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type credential struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Secret    []byte    `json:"secret"`
	CreatedAt time.Time `json:"createdAt"`
}

type identity struct {
	Version int                   `json:"version"`
	Private []byte                `json:"private"`
	Devices map[string]credential `json:"devices"`
}

type protector interface {
	protect([]byte) ([]byte, error)
	unprotect([]byte) ([]byte, error)
}

type identityStore struct {
	path string
	box  protector
}

func (s identityStore) load() (identity, *ecdsa.PrivateKey, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return identity{}, nil, err
		}
		private, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return identity{}, nil, err
		}
		state := identity{Version: 1, Private: private, Devices: map[string]credential{}}
		if err = s.save(state); err != nil {
			return identity{}, nil, err
		}
		return state, k, nil
	}
	if err != nil {
		return identity{}, nil, err
	}
	defer f.Close()
	sealed, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(sealed) > 64*1024 {
		return identity{}, nil, errors.New("invalid protected identity size")
	}
	plain, err := s.box.unprotect(sealed)
	if err != nil {
		return identity{}, nil, fmt.Errorf("unprotect identity: %w", err)
	}
	defer clear(plain)
	var state identity
	if err = strictJSON(plain, &state, 32*1024); err != nil {
		return identity{}, nil, err
	}
	if state.Version != 1 || len(state.Devices) > 16 || state.Devices == nil {
		return identity{}, nil, errors.New("invalid identity state")
	}
	k, err := x509.ParseECPrivateKey(state.Private)
	if err != nil || k.Curve != elliptic.P256() {
		return identity{}, nil, errors.New("invalid host identity key")
	}
	for id, c := range state.Devices {
		if !validID(id) || c.ID != id || len(c.Secret) != 32 || !validName(c.Name) || c.CreatedAt.IsZero() {
			return identity{}, nil, errors.New("invalid saved device")
		}
	}
	return state, k, nil
}

func (s identityStore) save(state identity) error {
	plain, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, err := s.box.protect(plain)
	if err != nil {
		return fmt.Errorf("protect identity: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	next := s.path + "." + id + ".new"
	f, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(next)
	_, err = f.Write(sealed)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceIdentity(next, s.path)
}

func copyIdentity(state identity) identity {
	next := state
	next.Devices = make(map[string]credential, len(state.Devices))
	for id, c := range state.Devices {
		next.Devices[id] = c
	}
	return next
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 80 || strings.TrimSpace(name) != name || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}
