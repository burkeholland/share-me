package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type DesktopSettings struct {
	StartWithWindows bool `json:"startWithWindows"`
	StartMinimized   bool `json:"startMinimized"`
	MinimizeToTray   bool `json:"minimizeToTray"`
}

type preferences struct {
	IP              string `json:"ip"`
	Transport       string `json:"transport"`
	ServiceURL      string `json:"serviceUrl,omitempty"`
	ShortcutEnabled bool   `json:"shortcutEnabled,omitempty"`
	DesktopSettings
}

func (a *App) loadPreferences() error {
	if a.prefsLoaded {
		return a.prefsErr
	}
	a.prefsLoaded = true
	if a.dataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			a.prefsErr = fmt.Errorf("find app settings folder: %w", err)
			return a.prefsErr
		}
		a.dataDir = filepath.Join(dir, "ShareMe")
	}
	a.prefs, a.prefsErr = readPreferences(a.dataDir)
	return a.prefsErr
}

func readPreferences(dir string) (preferences, error) {
	pref := preferences{Transport: "secure", DesktopSettings: DesktopSettings{MinimizeToTray: true}}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return pref, nil
	}
	if err != nil {
		return pref, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(data, &pref); err != nil {
		return pref, fmt.Errorf("settings.json is not valid JSON: %w", err)
	}
	return pref, nil
}

func savePreferences(dir string, pref preferences) (err error) {
	data, err := json.Marshal(pref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "settings-*.tmp")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("clean up settings file: %w", removeErr))
		}
	}()
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, "settings.json"))
}

type startupRegistration struct {
	Exists  bool
	Command string
	Expand  bool
}

type startupStore interface {
	Read() (startupRegistration, error)
	Write(startupRegistration) error
}

func commitDesktopSettings(dir string, current preferences, next DesktopSettings, store startupStore, executable string) (preferences, error) {
	updated := current
	updated.DesktopSettings = next
	previous, err := store.Read()
	if err != nil {
		return current, fmt.Errorf("read Windows startup setting: %w", err)
	}
	command, err := startupCommand(executable)
	if err != nil {
		return current, err
	}
	desired := startupRegistration{}
	if next.StartWithWindows {
		desired = startupRegistration{Exists: true, Command: command}
	}
	if previous != desired {
		if err := store.Write(desired); err != nil {
			return current, fmt.Errorf("change Windows startup setting: %w", err)
		}
	}
	if err := savePreferences(dir, updated); err != nil {
		if previous != desired {
			if restoreErr := store.Write(previous); restoreErr != nil {
				return current, errors.Join(fmt.Errorf("save settings: %w", err), fmt.Errorf("restore Windows startup setting: %w; check Windows Startup apps", restoreErr))
			}
		}
		return current, fmt.Errorf("save settings: %w", err)
	}
	return updated, nil
}

func (a *App) SetDesktopSettings(settings DesktopSettings) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.loadPreferences(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable for Windows startup: %w", err)
	}
	next, err := commitDesktopSettings(a.dataDir, a.prefs, settings, windowsStartupStore{}, executable)
	if err != nil {
		a.settingsErr = err.Error()
		return err
	}
	a.prefs = next
	a.settingsErr = ""
	return nil
}
