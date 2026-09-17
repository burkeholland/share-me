package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnErrorReportsApprovedScannerAndStorageFailuresOutsideLocks(t *testing.T) {
	scanFailure := errors.New("Defender rejected the file")
	for _, mode := range []string{"scanner", "scanner-timeout", "file-storage", "text-storage"} {
		t.Run(mode, func(t *testing.T) {
			var service atomic.Pointer[Service]
			notifications := make(chan error, 4)
			callbackFailures := make(chan error, 4)
			f := newFixture(t, 128, func(config *Config) {
				config.OnError = func(err error) {
					current := service.Load()
					status := current.Status()
					_ = current.Pending()
					_, _ = current.List()
					if status.Error == "" {
						callbackFailures <- errors.New("OnError ran before native error status was updated")
					}
					notifications <- err
				}
				switch mode {
				case "scanner":
					config.ScanFile = func(context.Context, string) error { return scanFailure }
				case "scanner-timeout":
					config.ScanFile = func(context.Context, string) error { return context.DeadlineExceeded }
				}
			})
			service.Store(f.service)
			if strings.HasSuffix(mode, "-storage") {
				if err := os.Mkdir(filepath.Join(f.config.DataDir, "state.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			route, contentType, body := "/api/text", "application/json", []byte(`{"text":"accepted text"}`)
			if mode != "text-storage" {
				route = "/api/upload"
				body, contentType = multipartBytes(t, partSpec{field: "file", name: "accepted.jpg", data: "photo", file: true})
			}
			result := f.approved(t, route, contentType, body)
			status := http.StatusInternalServerError
			if strings.HasPrefix(mode, "scanner") {
				status = http.StatusUnprocessableEntity
			}
			assertStatus(t, result, status)
			object(t, result, "error")
			var nativeError error
			select {
			case nativeError = <-notifications:
			case <-time.After(time.Second):
				t.Fatal("accepted transfer failure did not call OnError")
			}
			switch mode {
			case "scanner":
				if !errors.Is(nativeError, scanFailure) || !strings.Contains(nativeError.Error(), scanFailure.Error()) {
					t.Fatal("native scanner error lost its cause", nativeError)
				}
			case "scanner-timeout":
				if !errors.Is(nativeError, context.DeadlineExceeded) {
					t.Fatal("scanner timeout was not reported as an operational failure", nativeError)
				}
			default:
				if !strings.Contains(nativeError.Error(), "replace transfer metadata") {
					t.Fatal("native storage error lost its details", nativeError)
				}
			}
			if bytes.Contains(result.body, []byte("state.json")) ||
				bytes.Contains(result.body, []byte("Defender rejected")) {
				t.Fatal("native-only error details leaked to the HTTP response")
			}
			waitPending(t, f, 0)
			waitFor(t, func() bool { return len(f.service.uploads) == 0 })
			assertUnpublished(t, f)
			select {
			case err := <-callbackFailures:
				t.Fatal(err)
			default:
			}
			select {
			case err := <-notifications:
				t.Fatal("OnError fired more than once for one transfer", err)
			default:
			}
		})
	}
}

func TestOnErrorExcludesDeclinesValidationTimeoutsAndSuccess(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.OnError = func(error) { calls.Add(1) }
		config.ApprovalTimeout = 100 * time.Millisecond
	})
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":""}`)), http.StatusBadRequest)
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "blocked.exe", data: "no", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body), http.StatusUnsupportedMediaType)
	result := f.begin(t, "POST", "/api/text", "application/json", []byte(`{"text":"decline"}`))
	pending := waitPending(t, f, 1)[0]
	if err := f.service.Decide(pending.ID, false); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, finish(t, result), http.StatusForbidden)
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", []byte(`{"text":"timeout"}`)), http.StatusRequestTimeout)
	body, contentType = multipartBytes(t, partSpec{field: "file", name: "too-large.jpg", data: strings.Repeat("a", 129), file: true})
	assertStatus(t, f.approved(t, "/api/upload", contentType, body), http.StatusRequestEntityTooLarge)
	receiptItem(t, f, f.approved(t, "/api/text", "application/json", []byte(`{"text":"success"}`)))
	waitFor(t, func() bool { return len(f.service.uploads) == 0 })
	if calls.Load() != 0 {
		t.Fatal("OnError reported a decline, validation error, approval timeout, or success")
	}
}

func TestOnErrorExcludesScannerCancelledByStop(t *testing.T) {
	var calls atomic.Int32
	scanning := make(chan struct{})
	f := newFixture(t, 128, func(config *Config) {
		config.OnError = func(error) { calls.Add(1) }
		config.ScanFile = func(ctx context.Context, _ string) error {
			close(scanning)
			<-ctx.Done()
			return fmt.Errorf("scan interrupted: %w", ctx.Err())
		}
	})
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "cancel.jpg", data: "photo", file: true})
	f.begin(t, "POST", "/api/upload", contentType, body)
	pending := waitPending(t, f, 1)[0]
	if err := f.service.Decide(pending.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-scanning:
	case <-time.After(time.Second):
		t.Fatal("scanner did not start")
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("intentional Stop triggered an error toast")
	}
	assertUnpublished(t, f)
}

func TestOnErrorExcludesUnavailableScannerBeforeApproval(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, 128, func(config *Config) {
		config.ScanFile = nil
		config.OnError = func(error) { calls.Add(1) }
	})
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "unavailable.jpg", data: "photo", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body), http.StatusServiceUnavailable)
	waitFor(t, func() bool { return len(f.service.uploads) == 0 })
	if calls.Load() != 0 || len(f.service.Pending()) != 0 {
		t.Fatal("a pre-approval scanner availability error was reported as an accepted transfer failure")
	}
	assertUnpublished(t, f)
}
