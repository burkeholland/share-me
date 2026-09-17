package shortcut

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const (
	maxKeys        = 64
	maxPublicKey   = 2048
	maxPlainState  = 256 * 1024
	maxSealedState = 512 * 1024
	stateFile      = "shortcut.identity"
)

type protector interface {
	protect([]byte) ([]byte, error)
	unprotect([]byte) ([]byte, error)
}

type identity struct {
	Version int               `json:"version"`
	Private []byte            `json:"private"`
	Keys    map[string]string `json:"keys"`
}

func (state identity) copy() identity {
	next := state
	next.Keys = make(map[string]string, len(state.Keys))
	for key, device := range state.Keys {
		next.Keys[key] = device
	}
	return next
}

type identityStore struct {
	path string
	box  protector
}

func publicKeyID(key ssh.PublicKey) (string, error) {
	if len(key.Marshal()) > maxPublicKey {
		return "", errors.New("unsupported public key")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
	case ssh.KeyAlgoRSA:
		cryptoKey, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return "", errors.New("unsupported public key")
		}
		rsaKey, ok := cryptoKey.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || rsaKey.N.BitLen() < 2048 || rsaKey.N.BitLen() > 8192 {
			return "", errors.New("RSA key must be between 2048 and 8192 bits")
		}
	default:
		return "", errors.New("unsupported public key")
	}
	return base64.StdEncoding.EncodeToString(key.Marshal()), nil
}

func (store identityStore) load() (identity, ssh.Signer, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return identity{}, nil, err
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			return identity{}, nil, err
		}
		state := identity{Version: 1, Private: encoded, Keys: map[string]string{}}
		signer, err := ssh.NewSignerFromKey(private)
		if err == nil {
			err = store.save(state)
		}
		return state, signer, err
	}
	if err != nil {
		return identity{}, nil, err
	}
	defer file.Close()
	sealed, err := readBounded(file, maxSealedState)
	if err != nil {
		return identity{}, nil, errors.New("invalid protected shortcut identity size")
	}
	plain, err := store.box.unprotect(sealed)
	if err != nil {
		return identity{}, nil, fmt.Errorf("unprotect shortcut identity: %w", err)
	}
	defer clear(plain)
	if len(plain) > maxPlainState {
		return identity{}, nil, errors.New("invalid shortcut identity size")
	}
	if !validStateJSON(plain) {
		return identity{}, nil, errors.New("invalid or ambiguous shortcut identity JSON")
	}
	var state identity
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&state); err != nil {
		return identity{}, nil, errors.New("invalid shortcut identity JSON")
	}
	if decoder.Decode(new(any)) != io.EOF || state.Version != 1 || state.Keys == nil || len(state.Keys) > maxKeys {
		return identity{}, nil, errors.New("invalid shortcut identity")
	}
	private, err := x509.ParsePKCS8PrivateKey(state.Private)
	if err != nil {
		return identity{}, nil, errors.New("invalid shortcut host key")
	}
	if _, ok := private.(ed25519.PrivateKey); !ok {
		return identity{}, nil, errors.New("invalid shortcut host key type")
	}
	for encoded, device := range state.Keys {
		wire, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(wire) > maxPublicKey || !validID(device) {
			return identity{}, nil, errors.New("invalid enrolled shortcut key")
		}
		key, err := ssh.ParsePublicKey(wire)
		if err != nil {
			return identity{}, nil, errors.New("invalid enrolled shortcut public key")
		}
		canonical, err := publicKeyID(key)
		if err != nil || canonical != encoded {
			return identity{}, nil, errors.New("invalid enrolled shortcut key encoding")
		}
	}
	signer, err := ssh.NewSignerFromKey(private)
	return state, signer, err
}

// Prune before publishing the Server: callbacks run without server locks, and
// an incomplete authorization check or failed save cannot enable the receiver.
func (store identityStore) prune(state identity, isDeviceAllowed func(string) bool) (identity, error) {
	next := state.copy()
	allowed := make(map[string]bool)
	for key, device := range state.Keys {
		permitted, checked := allowed[device]
		if !checked {
			var err error
			permitted, err = checkDeviceAllowed(isDeviceAllowed, device)
			if err != nil {
				return identity{}, err
			}
			allowed[device] = permitted
		}
		if !permitted {
			delete(next.Keys, key)
		}
	}
	if len(next.Keys) == len(state.Keys) {
		return state, nil
	}
	if err := store.save(next); err != nil {
		return identity{}, fmt.Errorf("persist pruned shortcut identity: %w", err)
	}
	return next, nil
}

func validStateJSON(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 4 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		if delimiter != '{' {
			return false
		}
		keys := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || keys[key] || len(keys) > maxKeys {
				return false
			}
			keys[key] = true
			if !value(depth + 1) {
				return false
			}
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim('}')
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func (store identityStore) save(state identity) error {
	plain, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(plain)
	if len(plain) > maxPlainState {
		return errors.New("shortcut identity is too large")
	}
	sealed, err := store.box.protect(plain)
	if err != nil {
		return fmt.Errorf("protect shortcut identity: %w", err)
	}
	if len(sealed) == 0 || len(sealed) > maxSealedState {
		return errors.New("invalid protected shortcut identity")
	}
	if err = os.MkdirAll(filepath.Dir(store.path), 0700); err != nil {
		return err
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return err
	}
	next := store.path + "." + hex.EncodeToString(suffix[:]) + ".new"
	file, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(next)
	_, err = file.Write(sealed)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceIdentity(next, store.path)
}
