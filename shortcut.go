package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"shareme/internal/shortcut"
)

func (a *App) startShortcutLocked() error {
	if a.engine == nil || a.service == nil || !a.service.Status().Running {
		return errors.New("start encrypted receiving first")
	}
	if a.shortcut != nil {
		if a.shortcut.Status().Running {
			return nil
		}
		if err := a.stopShortcutLocked(); err != nil {
			return err
		}
	}
	engine := a.engine
	port := shortcut.DefaultPort
	if a.shortcutTest {
		port = 0
	}
	server, err := shortcut.New(shortcut.Config{
		DataDir: a.dataDir, LocalIP: a.activeIP, Port: port,
		AllowLoopback: a.shortcutTest, MaxFileBytes: 2 << 30,
		IsDeviceAllowed: func(id string) bool {
			for _, device := range engine.Devices() {
				if device.ID == id {
					return true
				}
			}
			return false
		},
		Handle:   a.service.HandleShortcut,
		OnError:  func(err error) { go a.notifyError(err) },
		OnChange: func() { go a.notifyTransfers() },
	})
	if err != nil {
		return err
	}
	ctx := a.workCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := server.Start(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	a.shortcut, a.shortcutErr = server, ""
	return nil
}

func (a *App) stopShortcutLocked() error {
	if a.shortcut == nil {
		return nil
	}
	err := a.shortcut.Close()
	a.shortcut = nil
	return err
}

func (a *App) setShortcutEnabledLocked(enabled bool) error {
	if err := a.loadPreferences(); err != nil {
		return err
	}
	next := a.prefs
	next.ShortcutEnabled = enabled
	if enabled {
		if err := a.startShortcutLocked(); err != nil {
			return err
		}
	}
	if next.ShortcutEnabled != a.prefs.ShortcutEnabled {
		if err := savePreferences(a.dataDir, next); err != nil {
			if enabled {
				err = errors.Join(err, a.stopShortcutLocked())
			}
			return fmt.Errorf("save Share Sheet setting: %w", err)
		}
	}
	a.prefs = next
	if !enabled {
		if err := a.stopShortcutLocked(); err != nil {
			return fmt.Errorf("stop Share Sheet receiver: %w", err)
		}
	}
	a.shortcutErr = ""
	return nil
}

func (a *App) SetShortcutEnabled(enabled bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setShortcutEnabledLocked(enabled)
}

func (a *App) createShortcutSetup(deviceID string) (shortcut.Setup, error) {
	// Receiver shutdown joins HTTP handlers while holding this mutex.
	// Setup must not wait for it from inside one of those handlers.
	if !a.mu.TryLock() {
		return shortcut.Setup{}, errors.New("Share Me is busy; try connecting the Shortcut again")
	}
	defer a.mu.Unlock()
	if _, err := a.selectedPhone(deviceID); err != nil {
		return shortcut.Setup{}, err
	}
	name, err := os.Hostname()
	if err != nil {
		return shortcut.Setup{}, fmt.Errorf("read PC name: %w", err)
	}
	if err := a.setShortcutEnabledLocked(true); err != nil {
		a.shortcutErr = err.Error()
		return shortcut.Setup{}, err
	}
	return a.shortcut.CreateSetup(deviceID, name)
}
