package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type trayLifecycle interface {
	Ready() bool
	Stop() error
}

type desktopWindow interface {
	Show(context.Context)
	Hide(context.Context)
	Minimized(context.Context) bool
	Quit(context.Context)
}

type wailsWindow struct{}

func (wailsWindow) Show(ctx context.Context) {
	wailsruntime.WindowUnminimise(ctx)
	wailsruntime.WindowShow(ctx)
}
func (wailsWindow) Hide(ctx context.Context)           { wailsruntime.WindowHide(ctx) }
func (wailsWindow) Minimized(ctx context.Context) bool { return wailsruntime.WindowIsMinimised(ctx) }
func (wailsWindow) Quit(ctx context.Context)           { wailsruntime.Quit(ctx) }

func (a *App) domReady(ctx context.Context) {
	a.mu.Lock()
	if a.domLoaded || a.closed {
		a.mu.Unlock()
		return
	}
	a.domLoaded = true
	a.mu.Unlock()
	tray, err := StartWindowsTray(TrayCallbacks{
		Open: a.showWindow,
		Hide: func() {
			if err := a.MinimizeToTray(); err != nil {
				a.reportTrayError(err)
			}
		},
		Quit:  a.Quit,
		Error: a.reportTrayError,
	})
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		if tray != nil {
			if err := tray.Stop(); err != nil {
				log.Printf("stop system tray: %v", err)
			}
		}
		return
	}
	if err == nil {
		a.tray = tray
	} else {
		a.trayErr = fmt.Sprintf("System tray unavailable: %v. Close will quit Share Me.", err)
		log.Print(a.trayErr)
	}
	a.domLoaded = true
	if a.prefsErr == nil && a.prefs.StartWithWindows {
		executable, startupErr := os.Executable()
		if startupErr == nil {
			startupErr = refreshStartupRegistration(executable)
		}
		if startupErr != nil {
			a.settingsErr = startupErr.Error()
			log.Printf("Windows startup: %v", startupErr)
		}
	}
	show := a.shouldShowInitially()
	monitorCtx, cancel := context.WithCancel(ctx)
	a.cancelMonitor = cancel
	a.mu.Unlock()
	if show {
		a.showWindow()
	} else if err := a.MinimizeToTray(); err != nil {
		a.reportTrayError(err)
	}
	go a.monitorMinimize(monitorCtx)
}

func (a *App) shouldShowInitially() bool {
	return !a.prefs.StartMinimized || a.showRequested || a.loadErr != "" ||
		a.settingsErr != "" || a.trayErr != "" || a.tray == nil || !a.tray.Ready()
}

func (a *App) showWindow() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.mu.Lock()
	if a.closed || a.quitting {
		a.mu.Unlock()
		return
	}
	a.showRequested = true
	if !a.domLoaded || a.ctx == nil {
		a.mu.Unlock()
		return
	}
	a.hidden = false
	ctx, window := a.ctx, a.window
	a.mu.Unlock()
	window.Show(ctx)
}

func (a *App) MinimizeToTray() error {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.mu.Lock()
	if a.closed || a.quitting || a.ctx == nil || !a.domLoaded {
		a.mu.Unlock()
		return errors.New("Share Me is not ready to minimize")
	}
	if a.tray == nil || !a.tray.Ready() {
		a.mu.Unlock()
		return errors.New("system tray is unavailable; keep the window open to receive transfers")
	}
	a.hidden = true
	ctx, window := a.ctx, a.window
	a.mu.Unlock()
	window.Hide(ctx)
	return nil
}

func (a *App) beforeClose(context.Context) bool {
	a.mu.Lock()
	exit := a.quitting || a.closed || a.tray == nil || !a.tray.Ready()
	a.mu.Unlock()
	if exit {
		return false
	}
	if err := a.MinimizeToTray(); err != nil {
		log.Printf("close to tray: %v", err)
		return false
	}
	return true
}

func (a *App) Quit() {
	a.mu.Lock()
	if a.closed || a.quitting || a.ctx == nil {
		a.mu.Unlock()
		return
	}
	a.quitting = true
	ctx, window := a.ctx, a.window
	a.mu.Unlock()
	window.Quit(ctx)
}

func (a *App) reportTrayError(err error) {
	a.mu.Lock()
	a.trayErr = fmt.Sprintf("System tray: %v. Keep Share Me open.", err)
	a.mu.Unlock()
	log.Printf("System tray: %v", err)
	a.showWindow()
}

func (a *App) monitorMinimize(ctx context.Context) {
	// Wails 2 has no minimize event. Poll the native state, not background WebView timers.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.handleMinimizedWindow()
		}
	}
}

func (a *App) handleMinimizedWindow() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.mu.Lock()
	if a.closed || a.quitting || a.hidden || !a.domLoaded || a.ctx == nil ||
		!a.prefs.MinimizeToTray || a.tray == nil || !a.tray.Ready() {
		a.mu.Unlock()
		return
	}
	ctx, window := a.ctx, a.window
	a.mu.Unlock()
	if window.Minimized(ctx) {
		a.mu.Lock()
		a.hidden = true
		a.mu.Unlock()
		window.Hide(ctx)
	}
}
