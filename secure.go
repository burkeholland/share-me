package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"shareme/internal/peer"
)

// Set by the publisher's build, not by individual users.
var defaultServiceURL string

func (a *App) startTransport(ip string) error {
	a.activeIP = ip
	if a.prefs.Transport == "local" {
		return a.service.Start(ip, 49321)
	}
	origin := a.prefs.ServiceURL
	if origin == "" {
		origin = defaultServiceURL
	}
	if origin == "" {
		return errors.New("this build needs its HTTPS connection-service address; build with scripts\\build.ps1 -ServiceURL https://your-service.workers.dev")
	}
	engine, err := peer.New(peer.Config{
		DataDir: a.dataDir, ServiceURL: origin, LocalIP: ip,
		OnChange: func() { go a.notifyTransfers() },
		OnError:  func(err error) { go a.notifyError(err) },
	})
	if err != nil {
		return err
	}
	ctx := a.workCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := engine.Start(ctx); err != nil {
		return errors.Join(err, engine.Close())
	}
	if err := a.service.StartPeer(engine.Listener(), engine.PhoneURL()); err != nil {
		return errors.Join(err, engine.Close())
	}
	a.engine = engine
	a.pairURL = ""
	if len(engine.Devices()) == 0 {
		link, err := engine.BeginPairing()
		if err != nil {
			return errors.Join(err, a.service.Stop(), engine.Close())
		}
		a.pairURL = link
	}
	return nil
}

func (a *App) AddPhone() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine == nil || a.service == nil || !a.service.Status().Running {
		return errors.New("start encrypted receiving first")
	}
	link, err := a.engine.BeginPairing()
	if err != nil {
		return err
	}
	a.pairURL = link
	return nil
}

func (a *App) CancelPairing() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine != nil {
		a.engine.CancelPairing()
	}
	a.pairURL = ""
}

func (a *App) DecidePair(id string, accept bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine == nil {
		return errors.New("encrypted receiving is not initialized")
	}
	if err := a.engine.DecidePair(id, accept); err != nil {
		return err
	}
	if accept {
		a.pairURL = ""
	}
	return nil
}

func (a *App) RenamePhone(id, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine == nil {
		return errors.New("encrypted receiving is not initialized")
	}
	return a.engine.RenameDevice(id, name)
}

func (a *App) RevokePhone(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engine == nil || a.outbox == nil {
		return errors.New("encrypted receiving is not initialized")
	}
	if err := a.engine.RevokeDevice(id); err != nil {
		return err
	}
	var result error
	for _, item := range a.outbox.List(id) {
		result = errors.Join(result, a.outbox.Remove(item.ID))
	}
	if len(a.engine.Devices()) == 0 {
		link, err := a.engine.BeginPairing()
		if err != nil {
			return errors.Join(result, fmt.Errorf("phone removed, but a new pairing QR could not be created: %w", err))
		}
		a.pairURL = link
	}
	return result
}

func (a *App) selectedPhone(id string) (peer.Device, error) {
	if a.engine != nil {
		for _, device := range a.engine.Devices() {
			if device.ID == id {
				return device, nil
			}
		}
	}
	return peer.Device{}, errors.New("choose a paired phone")
}

func (a *App) SendFiles(id string) error {
	a.mu.Lock()
	device, err := a.selectedPhone(id)
	ctx, workCtx, store := a.ctx, a.workCtx, a.outbox
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if ctx == nil || workCtx == nil || store == nil {
		return errors.New("Share Me is not ready to send")
	}
	paths, err := wailsruntime.OpenMultipleFilesDialog(ctx, wailsruntime.OpenDialogOptions{
		Title: "Send to " + device.Name,
	})
	if err != nil {
		return fmt.Errorf("choose files: %w", err)
	}
	for index, path := range paths {
		a.mu.Lock()
		_, err := a.selectedPhone(id)
		a.mu.Unlock()
		if err != nil {
			return err
		}
		item, err := store.AddFile(workCtx, id, path)
		if err != nil {
			return fmt.Errorf("%d files queued; could not prepare the next file: %w", index, err)
		}
		a.mu.Lock()
		_, err = a.selectedPhone(id)
		a.mu.Unlock()
		if err != nil {
			return errors.Join(err, store.Remove(item.ID))
		}
		a.notifyTransfers()
	}
	return nil
}

func (a *App) SendClipboardText(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.selectedPhone(id); err != nil {
		return err
	}
	if a.ctx == nil || a.workCtx == nil || a.outbox == nil {
		return errors.New("Share Me is not ready to send")
	}
	text, err := wailsruntime.ClipboardGetText(a.ctx)
	if err != nil {
		return fmt.Errorf("read clipboard text: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the clipboard contains no text")
	}
	_, err = a.outbox.AddText(a.workCtx, id, text)
	return err
}

func (a *App) RemoveOutgoing(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.outbox == nil {
		return errors.New("outbox is not initialized")
	}
	return a.outbox.Remove(id)
}
