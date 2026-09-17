package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const startupKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupValue = "ShareMe"

type windowsStartupStore struct{}

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
