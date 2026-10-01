package transfer

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferErrorClearsOnlyAfterDurableSuccess(t *testing.T) {
	for _, kind := range []string{"text", "file"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, 128)
			f.service.respondError(httptest.NewRecorder(), errors.New("earlier failure"), "")
			assertStatus(t, f.call(t, "GET", "/api/session", "", nil), http.StatusOK)
			if f.service.Status().Error != "earlier failure" {
				t.Fatal("GET cleared a transfer failure")
			}
			route, contentType, body := "/api/text", "application/json", []byte(`{"text":"success"}`)
			if kind == "file" {
				route = "/api/upload"
				body, contentType = multipartBytes(t, partSpec{field: "file", name: "success.jpg", data: "photo", file: true})
			}
			pendingResult := f.begin(t, "POST", route, contentType, body)
			pending := waitPending(t, f, 1)[0]
			if f.service.Status().Error != "earlier failure" {
				t.Fatal("pending approval cleared a transfer failure")
			}
			if err := f.service.Decide(pending.ID, true); err != nil {
				t.Fatal(err)
			}
			receiptItem(t, f, finish(t, pendingResult))
			if f.service.Status().Error != "" {
				t.Fatal("successful durable commit retained a transfer failure")
			}
			f.service.respondError(httptest.NewRecorder(), errors.New("newer failure"), "")
			assertStatus(t, f.call(t, "GET", "/api/session", "", nil), http.StatusOK)
			if f.service.Status().Error != "newer failure" {
				t.Fatal("later failure was overwritten")
			}
		})
	}
}

func TestFailedOrStoppedCommitDoesNotClearTransferError(t *testing.T) {
	f := newFixture(t, 128)
	if err := os.Mkdir(filepath.Join(f.config.DataDir, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	f.service.respondError(httptest.NewRecorder(), errors.New("receiver failure"), "")
	if _, err := f.service.storeText(t.Context(), strings.Repeat("a", 32), "Text", "text"); err == nil {
		t.Fatal("blocked metadata replacement succeeded")
	}
	if f.service.Status().Error != "receiver failure" {
		t.Fatal("failed metadata replacement cleared a receiver error")
	}
	if err := f.service.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.storeText(t.Context(), strings.Repeat("a", 32), "Text", "text"); err == nil {
		t.Fatal("stopped text commit succeeded")
	}
	if err := f.service.publishFile(t.Context(), "", Item{}); err == nil {
		t.Fatal("stopped file commit succeeded")
	}
	if f.service.Status().Error != "receiver failure" {
		t.Fatal("failed or stopped commit cleared a receiver error")
	}
}
