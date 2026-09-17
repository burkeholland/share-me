package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

func TestCryptoVectors(t *testing.T) {
	aead, err := signalAEAD(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(aead.Seal(nil, make([]byte, 12), nil, nil)); got != "530f8afbc74536b9a963b4f1c4cb738b" {
		t.Fatalf("NIST AES-256-GCM vector: %s", got)
	}
	env := envelope{
		Type: "signal", SID: strings.Repeat("b", 32), KID: "pair", Kind: "offer",
		IV: "AAAAAAAAAAAAAAAA", Data: "tYUzWT1CUUxzK7anmN-_dhMNZugNhEMkuc2b61d7j24QsAC6Hjme_QnMmVrhpg",
	}
	plain, err := openSignal(make([]byte, 32), strings.Repeat("a", 32), env)
	if err != nil || string(plain) != `{"sdp":"test","name":"iPhone"}` {
		t.Fatalf("Node AES-GCM interoperability vector: %s %v", plain, err)
	}
	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	key := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: big.NewInt(1)}
	if roomFor(key) != "698bea63dc44a344663ff1429aea1084" || len(publicBytes(key)) != 65 {
		t.Fatal("host identity vector mismatch")
	}
	challenge := raw64.EncodeToString(make([]byte, 32))
	signature, err := signChallenge(key, roomFor(key), challenge)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := decodeFixed(signature, 64)
	hash := sha256.Sum256([]byte("ShareMe host v1\n" + roomFor(key) + "\n" + challenge))
	if err != nil || !ecdsa.Verify(&key.PublicKey, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("P1363 signature failed")
	}
}

func TestSignalBindingAndValidation(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	room := strings.Repeat("a", 32)
	env, err := sealSignal(key, room, envelope{Type: "signal", SID: strings.Repeat("b", 32), KID: "pair", Kind: "offer"}, []byte(`{"sdp":"fingerprint","name":"phone"}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []envelope{env, env, env, env, env, env}
	cases[0].SID = strings.Repeat("c", 32)
	cases[1].KID = strings.Repeat("d", 32)
	cases[2].Kind = "answer"
	cases[3].IV = "bad"
	cases[4].Data = strings.Repeat("A", maxSignal)
	ciphertext, err := raw64.DecodeString(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 1
	cases[5].Data = raw64.EncodeToString(ciphertext)
	for i, test := range cases {
		if _, err := openSignal(key, room, test); err == nil {
			t.Fatalf("tampering accepted: %d", i)
		}
	}
	if _, err := openSignal(key, strings.Repeat("e", 32), env); err == nil {
		t.Fatal("room not bound")
	}
	if _, err := openSignal(make([]byte, 32), room, env); err == nil {
		t.Fatal("wrong key accepted")
	}
	for _, invalid := range []string{
		`{"type":"one","type":"two"}`, `{"unknown":true}`, `{"type":"one"}{}`,
		`{"type":"one"} null`, `null`, `{"type":["wrong"]}`,
	} {
		var v struct {
			Type string `json:"type"`
		}
		if err := strictJSON([]byte(invalid), &v, 256); err == nil {
			t.Fatalf("invalid JSON accepted: %s", invalid)
		}
	}
}
