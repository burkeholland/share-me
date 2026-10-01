package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const startupKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupValue = "ShareMe"

// startupTaskID must match the desktop:StartupTask TaskId in packaging\msix\AppxManifest.xml.in.
const startupTaskID = "ShareMeStartup"

type windowsStartupStore struct{}

func currentStartupStore() startupStore {
	if runningPackaged() {
		return packagedStartupStore{}
	}
	return windowsStartupStore{}
}

func startupCommand(executable string) (string, error) {
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\"\x00\r\n") {
		return "", errors.New("Windows startup needs an absolute executable path without quotes or newlines")
	}
	return `"` + executable + `" --startup`, nil
}

func (windowsStartupStore) Read() (startupRegistration, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return startupRegistration{}, nil
	}
	if err != nil {
		return startupRegistration{}, err
	}
	defer key.Close()
	command, kind, err := key.GetStringValue(startupValue)
	if errors.Is(err, registry.ErrNotExist) {
		return startupRegistration{}, nil
	}
	if err != nil {
		return startupRegistration{}, err
	}
	return startupRegistration{Exists: true, Command: command, Expand: kind == registry.EXPAND_SZ}, nil
}

func (windowsStartupStore) Write(value startupRegistration) error {
	if !value.Exists {
		key, err := registry.OpenKey(registry.CURRENT_USER, startupKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		if err := key.DeleteValue(startupValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if value.Expand {
		return key.SetExpandStringValue(startupValue, value.Command)
	}
	return key.SetStringValue(startupValue, value.Command)
}

// syncStartup runs at launch while Start with Windows is on. The ZIP build repairs a Run entry
// that points at an old location. The Store build cannot override Windows: when the startup task
// was turned off in Windows Settings, the preference follows it.
func syncStartup(dir string, current preferences) (preferences, error) {
	if runningPackaged() {
		registration, err := packagedStartupStore{}.Read()
		if err != nil || registration.Exists {
			return current, err
		}
		next := current
		next.StartWithWindows = false
		if err := savePreferences(dir, next); err != nil {
			return current, fmt.Errorf("save settings: %w", err)
		}
		return next, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return current, err
	}
	return current, refreshStartupRegistration(executable)
}

func refreshStartupRegistration(executable string) error {
	command, err := startupCommand(executable)
	if err != nil {
		return err
	}
	store := windowsStartupStore{}
	current, err := store.Read()
	if err != nil {
		return fmt.Errorf("read Windows startup setting: %w", err)
	}
	desired := startupRegistration{Exists: true, Command: command}
	if current == desired {
		return nil
	}
	if err := store.Write(desired); err != nil {
		return fmt.Errorf("update Windows startup location: %w", err)
	}
	return nil
}
