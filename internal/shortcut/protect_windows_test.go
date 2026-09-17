//go:build windows

package shortcut

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestProductionDPAPIIdentity(t *testing.T) {
	cfg := Config{
		DataDir: testDirectory(t), LocalIP: "127.0.0.1", AllowLoopback: true,
		IsDeviceAllowed: func(string) bool { return true },
		Handle: func(context.Context, Request) (Response, error) {
			return Response{StatusCode: 200, Body: []byte(`{}`)}, nil
		},
	}
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	data, err := os.ReadFile(first.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"private"`)) {
		t.Fatal("production identity stored as plaintext")
	}
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	if first.Status().Fingerprint != second.Status().Fingerprint {
		t.Fatal("DPAPI identity did not persist")
	}
	data[len(data)/2] ^= 1
	if err = os.WriteFile(first.store.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if third, err := New(cfg); err == nil {
		third.Close()
		t.Fatal("tampered DPAPI identity accepted")
	}
}
