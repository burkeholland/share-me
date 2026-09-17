package shortcut

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusJSONContract(t *testing.T) {
	server, err := newServer(Config{
		DataDir: testDirectory(t), LocalIP: "127.0.0.1", AllowLoopback: true,
		IsDeviceAllowed: func(string) bool { return true },
		Handle: func(context.Context, Request) (Response, error) {
			return Response{StatusCode: 200, Body: []byte(`{}`)}, nil
		},
	}, newTestProtector(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	check := func(running bool) {
		t.Helper()
		status := server.Status()
		if status.Running != running || status.Address == "" || status.Fingerprint == "" {
			t.Fatalf("unexpected status: %+v", status)
		}
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 || fields["running"] == nil || fields["address"] == nil || fields["fingerprint"] == nil {
			t.Fatalf("unexpected status JSON fields: %s", data)
		}
	}
	check(false)
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestResponse64KiBBoundaryOverSSH(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{
		{name: "exact limit", size: 64 * 1024},
		{name: "one byte over", size: 64*1024 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"data":"` + strings.Repeat("x", tc.size-len(`{"data":""}`)) + `"}`)
			if len(body) != tc.size {
				t.Fatal("incorrect boundary fixture")
			}
			h := newHarness(t, func(context.Context, Request) (Response, error) {
				return Response{StatusCode: 200, Body: body}, nil
			})
			key := signer(t)
			setup(t, h, deviceA, key)
			client := dial(t, h, "shareme", key)
			if tc.size > 64*1024 {
				assertExitFailure(t, client, "shareme-v1 request", []byte(`{}`))
				return
			}
			out, stderr, err := command(client, "shareme-v1 request", []byte(`{}`))
			if err != nil || stderr != "" || !bytes.Equal([]byte(out), append(body, '\n')) {
				t.Fatalf("exact response limit rejected or changed: stderr=%q error=%v bytes=%d", stderr, err, len(out))
			}
		})
	}
}
