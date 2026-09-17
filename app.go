package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v2/pkg/options"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"shareme/internal/outbox"
	"shareme/internal/peer"
	"shareme/internal/safety"
	"shareme/internal/shortcut"
	"shareme/internal/transfer"
)

type App struct {
	mu            sync.Mutex
	ctx           context.Context
	assets        fs.FS
	service       *transfer.Service
	dataDir       string
	loadErr       string
	qrURL         string
	qrImage       string
	notified      map[string]bool
	closed        bool
	prefs         preferences
	prefsLoaded   bool
	prefsErr      error
	settingsErr   string
	trayErr       string
	tray          trayLifecycle
	window        desktopWindow
	windowMu      sync.Mutex
	domLoaded     bool
	showRequested bool
	hidden        bool
	quitting      bool
	cancelMonitor context.CancelFunc
	engine        *peer.Engine
	outbox        *outbox.Store
	pairURL       string
	workCtx       context.Context
	cancelWork    context.CancelFunc
	activeIP      string
	shortcut      *shortcut.Server
	shortcutErr   string
	shortcutTest  bool
}

type ViewState struct {
	Status           transfer.Status            `json:"status"`
	Networks         []transfer.Network         `json:"networks"`
	Items            []transfer.Item            `json:"items"`
	QR               string                     `json:"qr"`
	Error            string                     `json:"error"`
	Pending          []transfer.PendingTransfer `json:"pending"`
	Settings         DesktopSettings            `json:"settings"`
	TrayAvailable    bool                       `json:"trayAvailable"`
	Secure           bool                       `json:"secure"`
	PhoneLink        string                     `json:"phoneLink"`
	ServiceConnected bool                       `json:"serviceConnected"`
	PeerMessage      string                     `json:"peerMessage"`
	Devices          []peer.Device              `json:"devices"`
	PairRequests     []peer.PairRequest         `json:"pairRequests"`
	Outbox           []outbox.Item              `json:"outbox"`
	NetworkIP        string                     `json:"networkIP"`
	ShortcutEnabled  bool                       `json:"shortcutEnabled"`
	ShortcutStatus   shortcut.Status            `json:"shortcutStatus"`
}

func (a *App) onSecondInstanceLaunch(options.SecondInstanceData) {
	a.showWindow()
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
	a.workCtx, a.cancelWork = context.WithCancel(ctx)
	if err := a.initialize(); err != nil {
		a.loadErr = err.Error()
		log.Printf("Share Me startup: %v", err)
	}
}

func (a *App) initialize() error {
	if err := a.loadPreferences(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find Downloads folder: %w", err)
	}
	a.outbox, err = outbox.New(filepath.Join(a.dataDir, "outbox"), 2<<30)
	if err != nil {
		return fmt.Errorf("load outgoing transfers: %w", err)
	}
	a.service, err = transfer.New(transfer.Config{
		DataDir: a.dataDir, InboxDir: filepath.Join(home, "Downloads", "Share Me"),
		Assets: a.assets, MaxFileBytes: 2 << 30,
		ScanFile:      safety.Scan,
		OnChange:      func() { go a.notifyTransfers() },
		OnError:       func(err error) { go a.notifyError(err) },
		Outbox:        a.outbox,
		ShortcutSetup: a.createShortcutSetup,
	})
	if err != nil {
		return err
	}
	networks, err := transfer.Networks()
	if err != nil {
		return err
	}
	if len(networks) == 0 {
		return errors.New("no local network found. Connect this PC to your home network, then click Refresh")
	}
	ip := a.prefs.IP
	available := false
	for _, network := range networks {
		available = available || network.IP == ip
	}
	if !available {
		ip = networks[0].IP
	}
	return a.startTransport(ip)
}

func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	a.closed = true
	service, tray, cancel, engine, cancelWork, shortcutServer := a.service, a.tray, a.cancelMonitor, a.engine, a.cancelWork, a.shortcut
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cancelWork != nil {
		cancelWork()
	}
	if shortcutServer != nil {
		if err := shortcutServer.Close(); err != nil {
			log.Printf("stop Shortcut receiver: %v", err)
		}
	}
	if tray != nil {
		if err := tray.Stop(); err != nil {
			log.Printf("stop system tray: %v", err)
		}
	}
	if service != nil {
		if err := service.Stop(); err != nil {
			log.Printf("stop receiver: %v", err)
		}
		if engine != nil {
			if err := engine.Close(); err != nil {
				log.Printf("stop encrypted peers: %v", err)
			}
		}
	}
}

func (a *App) GetState() (ViewState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := ViewState{Error: a.loadErr, Items: []transfer.Item{}, Networks: []transfer.Network{}}
	state.Settings = a.prefs.DesktopSettings
	state.Secure = a.prefs.Transport != "local"
	state.NetworkIP = a.activeIP
	state.ShortcutEnabled = a.prefs.ShortcutEnabled
	if a.shortcut != nil {
		state.ShortcutStatus = a.shortcut.Status()
	}
	state.Devices = []peer.Device{}
	state.PairRequests = []peer.PairRequest{}
	state.Outbox = []outbox.Item{}
	if a.outbox != nil {
		state.Outbox = a.outbox.List("")
	}
	if a.engine != nil {
		state.ServiceConnected, state.PeerMessage = a.engine.Status()
		state.Devices = a.engine.Devices()
		state.PairRequests = a.engine.PairRequests()
	}
	state.TrayAvailable = a.tray != nil && a.tray.Ready()
	var notices []string
	for _, notice := range []string{a.loadErr, a.settingsErr, a.trayErr, a.shortcutErr} {
		if notice != "" {
			notices = append(notices, notice)
		}
	}
	state.Error = strings.Join(notices, "\n")
	networks, err := transfer.Networks()
	if err != nil {
		return state, err
	}
	if networks != nil {
		state.Networks = networks
	}
	if a.service == nil {
		return state, nil
	}
	state.Status = a.service.Status()
	state.Pending = a.service.Pending()
	state.Items, err = a.service.List()
	if err != nil {
		return state, err
	}
	link := ""
	if state.Status.Running {
		link = state.Status.Address + "/"
		if state.Secure && a.engine != nil {
			link = a.engine.PhoneURL()
			if a.pairURL != "" {
				link = a.pairURL
			}
		}
	}
	state.PhoneLink = link
	if link != a.qrURL {
		a.qrURL = link
		a.qrImage = ""
		if a.qrURL != "" {
			png, err := qrcode.Encode(a.qrURL, qrcode.Medium, 320)
			if err != nil {
				return state, fmt.Errorf("create phone link: %w", err)
			}
			a.qrImage = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	state.QR = a.qrImage
	return state, nil
}

func (a *App) Start(ip string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil {
		return errors.New("receiver could not initialize. Close Share Me, correct the startup error, and reopen it")
	}
	if a.service.Status().Running {
		return errors.New("pause receiving before changing networks")
	}
	if err := a.loadPreferences(); err != nil {
		return err
	}
	if err := a.startTransport(ip); err != nil {
		return err
	}
	next := a.prefs
	next.IP = ip
	if err := savePreferences(a.dataDir, next); err != nil {
		stopErr := a.stopShortcutLocked()
		stopErr = errors.Join(stopErr, a.service.Stop())
		if a.engine != nil {
			stopErr = errors.Join(stopErr, a.engine.Close())
		}
		return errors.Join(fmt.Errorf("save network preference: %w", err), stopErr)
	}
	a.prefs = next
	a.loadErr = ""
	return nil
}

func (a *App) Pause() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil {
		return errors.New("receiver is not initialized")
	}
	err := a.stopShortcutLocked()
	err = errors.Join(err, a.service.Stop())
	if a.engine != nil {
		err = errors.Join(err, a.engine.Close())
	}
	a.pairURL = ""
	return err
}

func (a *App) Decide(id string, accept bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil {
		return errors.New("receiver is not initialized")
	}
	return a.service.Decide(id, accept)
}

func (a *App) notifyTransfers() {
	a.mu.Lock()
	if a.service == nil || a.ctx == nil || a.closed {
		a.mu.Unlock()
		return
	}
	next := map[string]bool{}
	show := false
	for _, pending := range a.service.Pending() {
		next[pending.ID] = true
		if pending.State == "pending" && !a.notified[pending.ID] {
			show = true
		}
	}
	if a.engine != nil {
		for _, request := range a.engine.PairRequests() {
			next[request.ID] = true
			if !a.notified[request.ID] {
				show = true
			}
		}
	}
	a.notified = next
	ctx := a.ctx
	a.mu.Unlock()
	if show {
		a.showWindow()
		if err := flashTransferWindow(); err != nil {
			log.Printf("Transfer attention: %v", err)
		}
	}
	wailsruntime.EventsEmit(ctx, "inbox:changed")
}

func (a *App) notifyError(err error) {
	a.mu.Lock()
	ctx := a.ctx
	closed := a.closed
	a.mu.Unlock()
	log.Printf("Transfer rejected: %v", err)
	if ctx != nil && !closed {
		wailsruntime.EventsEmit(ctx, "transfer:error", err.Error())
	}
}

func (a *App) CopyText(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil {
		return errors.New("receiver is not initialized")
	}
	item, err := a.service.Find(id)
	if err != nil {
		return err
	}
	if item.Kind != "text" {
		return errors.New("only text transfers can be copied to the clipboard")
	}
	return wailsruntime.ClipboardSetText(a.ctx, item.Text)
}

func (a *App) CopyLink() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil || !a.service.Status().Running {
		return errors.New("start receiving first")
	}
	link := a.service.Status().Address + "/"
	if a.prefs.Transport != "local" && a.engine != nil {
		link = a.engine.PhoneURL()
		if a.pairURL != "" {
			link = a.pairURL
		}
	}
	return wailsruntime.ClipboardSetText(a.ctx, link)
}

func (a *App) OpenInbox() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil {
		return errors.New("receiver is not initialized")
	}
	path := a.service.Status().InboxDir
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("open inbox: %w", err)
	}
	command := exec.Command("explorer.exe", path)
	if err := command.Start(); err != nil {
		return fmt.Errorf("open inbox: %w", err)
	}
	go func() {
		if err := command.Wait(); err != nil {
			log.Printf("Explorer exited: %v", err)
		}
	}()
	return nil
}
