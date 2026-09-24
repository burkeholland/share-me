package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"shareme/internal/outbox"
	"shareme/internal/peer"
	"shareme/internal/safety"
	"shareme/internal/transfer"
)

func runPeerHeadless(assets fs.FS, args []string) error {
	flags := flag.NewFlagSet("ShareMe --headless-peer", flag.ContinueOnError)
	ip := flags.String("ip", "", "Explicit peer interface")
	origin := flags.String("service-url", "", "Signaling origin")
	data := flags.String("data", "", "Isolated test directory")
	statePath := flags.String("state-file", "", "Local bootstrap information file")
	dev := flags.Bool("dev-loopback", false, "Enable explicit local test mode")
	control := flags.Bool("control-stdio", false, "Local test decisions on stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *ip == "" || *origin == "" || *data == "" || !*dev || !*control || *statePath == "" {
		return errors.New("peer harness requires an explicit --ip, --service-url, --data, --state-file, --dev-loopback and --control-stdio")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	box, err := outbox.New(filepath.Join(*data, "outbox"), 2<<30)
	if err != nil {
		return err
	}
	engine, err := peer.New(peer.Config{
		DataDir: *data, ServiceURL: *origin, LocalIP: *ip, AllowLoopback: true,
		OnError: func(err error) { fmt.Fprintln(os.Stderr, err) },
	})
	if err != nil {
		return err
	}
	defer engine.Close()
	if err := engine.Start(ctx); err != nil {
		return err
	}
	app := &App{
		engine: engine, outbox: box, dataDir: *data, activeIP: *ip, workCtx: ctx,
		prefsLoaded: true, prefs: preferences{Transport: "secure"},
	}
	defer func() {
		if app.service != nil {
			if err := app.service.Stop(); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}()
	service, err := transfer.New(transfer.Config{
		DataDir: *data, InboxDir: filepath.Join(*data, "inbox"), Assets: assets,
		MaxFileBytes: 2 << 30, AllowLoopback: true, ScanFile: safety.Scan, Outbox: box,
	})
	if err != nil {
		return err
	}
	app.service = service
	if err := service.StartPeer(engine.Listener(), engine.PhoneURL()); err != nil {
		return err
	}
	pairURL, err := engine.BeginPairing()
	if err != nil {
		return err
	}
	app.pairURL = pairURL
	bootstrap, err := json.Marshal(map[string]string{"room": engine.Room(), "pairURL": pairURL, "dataDir": *data, "inboxDir": filepath.Join(*data, "inbox")})
	if err != nil {
		return err
	}
	if err := os.WriteFile(*statePath, bootstrap, 0600); err != nil {
		return err
	}
	commands := make(chan []byte)
	failures := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 128<<10)
		for scanner.Scan() {
			select {
			case commands <- append([]byte{}, scanner.Bytes()...):
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			failures <- err
		} else {
			cancel()
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failures:
			return err
		case raw := <-commands:
			var command struct {
				ID     string `json:"id"`
				Pair   string `json:"pair"`
				Accept bool   `json:"accept"`
				Device string `json:"device"`
				File   string `json:"file"`
				Text   string `json:"text"`
				Revoke string `json:"revoke"`
			}
			if err := json.Unmarshal(raw, &command); err != nil {
				return err
			}
			var err error
			switch {
			case command.Pair != "":
				err = app.DecidePair(command.Pair, command.Accept)
			case command.ID != "":
				err = service.Decide(command.ID, command.Accept)
			case command.Revoke != "":
				err = app.RevokePhone(command.Revoke)
			case command.Device != "" && command.File != "":
				_, err = box.AddFile(ctx, command.Device, command.File)
			case command.Device != "" && command.Text != "":
				_, err = box.AddText(ctx, command.Device, command.Text)
			default:
				err = errors.New("unknown local peer harness command")
			}
			if err != nil {
				return err
			}
		case <-ticker.C:
			connected, message := engine.Status()
			frame, err := json.Marshal(map[string]any{
				"type": "peer-state", "connected": connected, "message": message,
				"pending": service.Pending(), "pairs": engine.PairRequests(),
				"devices": engine.Devices(), "outbox": box.List(""),
				"pairURL": app.pairURL,
			})
			if err != nil {
				return err
			}
			if string(frame) != last {
				if _, err := fmt.Fprintln(os.Stdout, string(frame)); err != nil {
					return err
				}
				last = string(frame)
			}
		}
	}
}
