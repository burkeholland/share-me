package transfer

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStopCancelsPendingReceivingAndScanning(t *testing.T) {
	for _, stage := range []string{"pending", "receiving", "scanning"} {
		t.Run(stage, func(t *testing.T) {
			scanned := make(chan struct{})
			f := newFixture(t, 1<<20, func(config *Config) {
				config.ScanFile = func(ctx context.Context, _ string) error {
					close(scanned)
					<-ctx.Done()
					// Even a scanner that returns nil after cancellation must
					// not allow a late publication.
					return nil
				}
			})
			if stage == "scanning" {
				body, contentType := multipartBytes(t, partSpec{field: "file", name: "scan.jpg", data: "photo", file: true})
				f.begin(t, "POST", "/api/upload", contentType, body)
			} else {
				dialPartialUpload(t, f)
			}
			pending := waitPending(t, f, 1)[0]
			if stage != "pending" {
				if err := f.service.Decide(pending.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "receiving" {
				waitFor(t, func() bool {
					entries, err := os.ReadDir(filepath.Join(f.config.InboxDir, ".shareme-quarantine"))
					return err == nil && len(entries) == 1
				})
			}
			if stage == "scanning" {
				select {
				case <-scanned:
				case <-time.After(2 * time.Second):
					t.Fatal("scanner did not start")
				}
			}
			start := time.Now()
			if err := f.service.Stop(); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("Stop failed to cancel the active stage promptly")
			}
			if len(f.service.Pending()) != 0 || len(f.service.uploads) != 0 || f.service.Status().Running {
				t.Fatal("Stop retained pending work")
			}
			if err := f.service.Decide(pending.ID, true); err == nil {
				t.Fatal("Stop allowed a stale decision")
			}
			assertUnpublished(t, f)
			if err := f.service.Start("127.0.0.1", 0); err != nil {
				t.Fatal(err)
			}
			result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"fresh approval"}`))
			fresh := waitPending(t, f, 1)[0]
			if err := f.service.Decide(fresh.ID, false); err != nil {
				t.Fatal(err)
			}
			assertStatus(t, finish(t, result), http.StatusForbidden)
		})
	}
}

func TestClientCancellationRemovesTextApproval(t *testing.T) {
	f := newFixture(t, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"cancel me"}`),
		func(r *http.Request) { *r = *r.WithContext(ctx) })
	pending := waitPending(t, f, 1)[0]
	cancel()
	select {
	case ended := <-result:
		if !errors.Is(ended.err, context.Canceled) {
			t.Fatal("client request was not cancelled", ended.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled client remained blocked")
	}
	waitPending(t, f, 0)
	if err := f.service.Decide(pending.ID, true); err == nil {
		t.Fatal("disconnected request could be approved")
	}
	assertUnpublished(t, f)
}

func TestLifecycleErrorsAndUnexpectedServeExit(t *testing.T) {
	f := newFixture(t, 128)
	address := f.service.Status().Address
	if err := f.service.Start("127.0.0.1", 0); err == nil || f.service.Status().Address != address {
		t.Fatal("duplicate Start replaced the running server")
	}
	f.service.mu.Lock()
	server := f.service.run.server
	f.service.mu.Unlock()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !f.service.Status().Running })
	if f.service.Status().Error == "" {
		t.Fatal("unexpected Serve exit was not reported")
	}
	for _, port := range []int{-1, 65536} {
		if err := f.service.Start("127.0.0.1", port); err == nil || f.service.Status().Error == "" {
			t.Fatal("invalid port not reported")
		}
	}
	if err := f.service.Start("127.0.0.1", 0); err != nil || f.service.Status().Error != "" {
		t.Fatal("restart did not clear the previous error", err)
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal("Stop is not idempotent", err)
	}
}

func TestConcurrentStartStopIsSerialized(t *testing.T) {
	f := newFixture(t, 128)
	var wait sync.WaitGroup
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = f.service.Start("127.0.0.1", 0)
			if err := f.service.Stop(); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if err := f.service.Stop(); err != nil || f.service.Status().Running {
		t.Fatal("concurrent lifecycle calls left server running", err)
	}
}

func TestNetworkAndBindValidation(t *testing.T) {
	networks, err := Networks()
	if err != nil {
		t.Fatal(err)
	}
	virtualSeen := false
	for _, network := range networks {
		ip, err := netip.ParseAddr(network.IP)
		if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("unusable address: %#v", network)
		}
		if virtualSeen && !virtualInterface(network.Name) {
			t.Fatal("physical interface sorted after a virtual interface")
		}
		virtualSeen = virtualSeen || virtualInterface(network.Name)
		if _, err := validateBindIP(network.IP, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, ip := range []string{"", "0.0.0.0", "8.8.8.8", "169.254.1.2", "::1", "::ffff:192.168.1.2", "127.0.0.1", "127.0.0.2"} {
		if _, err := validateBindIP(ip, false); err == nil {
			t.Fatal("unsafe bind accepted", ip)
		}
	}
	if _, err := validateBindIP("127.0.0.1", true); err != nil {
		t.Fatal(err)
	}
	root := testDirectory(t)
	service, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start("127.0.0.1", 0); err == nil {
		_ = service.Stop()
		t.Fatal("production config permitted loopback")
	}
	if !strings.HasPrefix(service.Status().InboxDir, root+string(filepath.Separator)) {
		t.Fatal("default inbox escaped its data directory")
	}
}
