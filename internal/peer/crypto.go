package peer

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxSignal = 32 * 1024

var raw64 = base64.RawURLEncoding.Strict()

type envelope struct {
	Type string `json:"type"`
	SID  string `json:"sid"`
	KID  string `json:"kid"`
	Kind string `json:"kind"`
	IV   string `json:"iv"`
	Data string `json:"data"`
}

type offer struct {
	SDP  string `json:"sdp"`
	Name string `json:"name"`
}

func strictJSON(data []byte, v any, limit int) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New("message exceeds size limit")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("JSON object required")
	}
	// Reject duplicate properties, including nested objects, before decoding.
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func uniqueJSON(d *json.Decoder) error {
	return uniqueJSONDepth(d, 0)
}

func uniqueJSONDepth(d *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid JSON delimiter")
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || keys[s] {
				return errors.New("duplicate JSON property")
			}
			keys[s] = true
		}
		if err := uniqueJSONDepth(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func randomID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

func publicBytes(k *ecdsa.PrivateKey) []byte {
	return elliptic.Marshal(elliptic.P256(), k.X, k.Y)
}

func roomFor(k *ecdsa.PrivateKey) string {
	hash := sha256.Sum256(publicBytes(k))
	return hex.EncodeToString(hash[:16])
}

func signChallenge(k *ecdsa.PrivateKey, room, challenge string) (string, error) {
	if _, err := decodeFixed(challenge, 32); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte("ShareMe host v1\n" + room + "\n" + challenge))
	r, s, err := ecdsa.Sign(rand.Reader, k, hash[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return raw64.EncodeToString(sig), nil
}

func decodeFixed(s string, n int) ([]byte, error) {
	if len(s) != raw64.EncodedLen(n) || strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("invalid base64url length")
	}
	b, err := raw64.DecodeString(s)
	if err != nil || len(b) != n {
		return nil, errors.New("invalid base64url")
	}
	return b, nil
}

func signalAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("invalid signaling key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func signalAAD(room string, e envelope) []byte {
	return []byte("ShareMe signal v1\n" + room + "\n" + e.SID + "\n" + e.KID + "\n" + e.Kind)
}

func validateEnvelope(e envelope) error {
	if e.Type != "signal" || !validID(e.SID) || e.KID != "pair" && !validID(e.KID) ||
		(e.Kind != "offer" && e.Kind != "answer") || len(e.Data) > maxSignal-256 {
		return errors.New("invalid signaling envelope")
	}
	return nil
}

func sealSignal(key []byte, room string, e envelope, plaintext []byte) (envelope, error) {
	aead, err := signalAEAD(key)
	if err != nil {
		return e, err
	}
	iv := make([]byte, 12)
	if _, err = rand.Read(iv); err != nil {
		return e, err
	}
	e.IV = raw64.EncodeToString(iv)
	e.Data = raw64.EncodeToString(aead.Seal(nil, iv, plaintext, signalAAD(room, e)))
	if err = validateEnvelope(e); err != nil {
		return e, err
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > maxSignal {
		return e, errors.New("signaling envelope too large")
	}
	return e, nil
}

func openSignal(key []byte, room string, e envelope) ([]byte, error) {
	if err := validateEnvelope(e); err != nil {
		return nil, err
	}
	iv, err := decodeFixed(e.IV, 12)
	if err != nil {
		return nil, err
	}
	if len(e.Data) < raw64.EncodedLen(16) || strings.ContainsAny(e.Data, "\r\n") {
		return nil, errors.New("invalid ciphertext")
	}
	data, err := raw64.DecodeString(e.Data)
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}
	aead, err := signalAEAD(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, iv, data, signalAAD(room, e))
	if err != nil {
		return nil, fmt.Errorf("unauthenticated signaling: %w", err)
	}
	return plain, nil
}
