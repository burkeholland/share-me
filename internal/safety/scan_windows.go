package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

func Scan(ctx context.Context, path string) error {
	return scan(ctx, path, defenderPath, runDefender, markAttachment)
}

func scan(ctx context.Context, path string, locate func() (string, error), run func(context.Context, string, string) error, mark func(string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file for safety check: %w", err)
	}
	header := make([]byte, 512)
	count, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read file for safety check: %w", readErr)
	}
	if closeErr != nil {
		return closeErr
	}
	header = bytes.TrimSpace(header[:count])
	if bytes.HasPrefix(header, []byte("MZ")) || bytes.HasPrefix(header, []byte("\x7fELF")) || bytes.HasPrefix(header, []byte("#!")) {
		return errors.New("executable content is not allowed")
	}
	scanner, err := locate()
	if err != nil {
		return err
	}
	scanContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := run(scanContext, scanner, path); err != nil {
		if scanContext.Err() != nil {
			return fmt.Errorf("file scan interrupted: %w", scanContext.Err())
		}
		return fmt.Errorf("Windows Defender did not clear this file. Check Windows Security: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mark(path); err != nil {
		return fmt.Errorf("could not apply Windows attachment protection: %w", err)
	}
	return nil
}

func defenderPath() (string, error) {
	location, registryErr := defenderInstallLocation()
	path, err := locateDefender(defenderCandidates(location, os.Getenv("ProgramFiles")), os.Stat)
	if err != nil {
		return "", errors.Join(err, registryErr)
	}
	return path, nil
}

func defenderInstallLocation() (location string, result error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows Defender`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("read Windows Defender location: %w", err)
	}
	defer func() { result = errors.Join(result, key.Close()) }()
	location, kind, err := key.GetStringValue("InstallLocation")
	if err != nil {
		return "", fmt.Errorf("read Windows Defender InstallLocation: %w", err)
	}
	if kind == registry.EXPAND_SZ {
		return registry.ExpandString(location)
	}
	return location, nil
}

func defenderCandidates(location, programFiles string) []string {
	var candidates []string
	if filepath.IsAbs(location) {
		candidates = append(candidates, filepath.Join(location, "MpCmdRun.exe"))
	}
	if filepath.IsAbs(programFiles) {
		candidates = append(candidates, filepath.Join(programFiles, "Windows Defender", "MpCmdRun.exe"))
	}
	return candidates
}

func locateDefender(candidates []string, stat func(string) (os.FileInfo, error)) (string, error) {
	var failures []error
	for _, path := range candidates {
		info, err := stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, nil
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("check scanner %s: %w", path, err))
		}
	}
	return "", errors.Join(append([]error{errors.New("Windows Defender is unavailable. Files cannot be accepted until scanning is available")}, failures...)...)
}

func runDefender(ctx context.Context, program, path string) error {
	command := exec.CommandContext(ctx, program, "-Scan", "-ScanType", "3", "-File", path, "-DisableRemediation")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func markAttachment(path string) error {
	if !filepath.IsAbs(path) || strings.Contains(filepath.Base(path), ":") {
		return errors.New("invalid attachment path")
	}
	file, err := os.OpenFile(path+":Zone.Identifier", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, "[ZoneTransfer]\r\nZoneId=3\r\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
