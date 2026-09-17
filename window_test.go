package main

import (
	"context"
	"errors"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

type fakeTray struct{ ready, stopped bool }

func (t *fakeTray) Ready() bool { return t.ready }
func (t *fakeTray) Stop() error { t.stopped, t.ready = true, false; return nil }

type fakeWindow struct {
	shown, hidden, quit int
	minimized           bool
}

func (w *fakeWindow) Show(context.Context)           { w.shown++; w.minimized = false }
func (w *fakeWindow) Hide(context.Context)           { w.hidden++ }
func (w *fakeWindow) Quit(context.Context)           { w.quit++ }
func (w *fakeWindow) Minimized(context.Context) bool { return w.minimized }

func windowTestApp() (*App, *fakeWindow, *fakeTray) {
	window, tray := &fakeWindow{}, &fakeTray{ready: true}
	return &App{
		ctx: context.Background(), domLoaded: true, window: window, tray: tray,
		prefs: preferences{DesktopSettings: DesktopSettings{MinimizeToTray: true}},
	}, window, tray
}

func TestCloseHidesAndQuitReallyExits(t *testing.T) {
	app, window, tray := windowTestApp()
	if !app.beforeClose(app.ctx) || !app.hidden || window.hidden != 1 || tray.stopped || app.closed {
		t.Fatal("Close must hide without stopping receiving or removing the tray")
	}
	app.onSecondInstanceLaunch(options.SecondInstanceData{})
	if app.hidden || window.shown != 1 {
		t.Fatal("a second launch must restore a hidden window")
	}
	app.Quit()
	app.Quit()
	if app.beforeClose(app.ctx) || window.quit != 1 {
		t.Fatal("explicit Quit must bypass close-to-tray exactly once")
	}
	app.shutdown(app.ctx)
	if !tray.stopped || !app.closed {
		t.Fatal("shutdown must remove the tray")
	}
}

func TestUnavailableTrayNeverHidesWindow(t *testing.T) {
	app, window, tray := windowTestApp()
	tray.ready = false
	if app.MinimizeToTray() == nil || app.beforeClose(app.ctx) || window.hidden != 0 {
		t.Fatal("without a tray, hiding must fail and Close must exit")
	}
	app.hidden = true
	app.reportTrayError(errors.New("Explorer recovery failed"))
	if app.hidden || window.shown != 1 || app.trayErr == "" {
		t.Fatal("tray failure must expose an actionable visible window")
	}
}

func TestNativeMinimizeHonorsPreference(t *testing.T) {
	app, window, tray := windowTestApp()
	window.minimized = true
	app.handleMinimizedWindow()
	app.handleMinimizedWindow()
	if window.hidden != 1 || !app.hidden {
		t.Fatal("native minimize must hide once, not repeatedly")
	}
	app.showWindow()
	app.prefs.MinimizeToTray = false
	window.minimized = true
	app.handleMinimizedWindow()
	if window.hidden != 1 {
		t.Fatal("disabled preference must leave native minimize on the taskbar")
	}
	app.prefs.MinimizeToTray = true
	tray.ready = false
	app.handleMinimizedWindow()
	if window.hidden != 1 {
		t.Fatal("native minimize must not hide without a working tray")
	}
}

func TestStartMinimizedKeepsErrorsAndExplicitOpensVisible(t *testing.T) {
	app, _, tray := windowTestApp()
	app.prefs.StartMinimized = true
	if app.shouldShowInitially() {
		t.Fatal("start minimized must leave a healthy app in the tray")
	}
	for _, field := range []*string{&app.loadErr, &app.settingsErr, &app.trayErr} {
		*field = "startup error"
		if !app.shouldShowInitially() {
			t.Fatal("startup errors must keep the window visible")
		}
		*field = ""
	}
	tray.ready = false
	if !app.shouldShowInitially() {
		t.Fatal("startup must remain visible if the tray is unavailable")
	}
	tray.ready = true
	app.showRequested = true
	if !app.shouldShowInitially() {
		t.Fatal("an early second launch or approval must override start minimized")
	}
}
