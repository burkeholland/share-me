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

	"shareme/internal/safety"
	"shareme/internal/transfer"
)

func runHeadless(assets fs.FS, args []string) error {
	flags := flag.NewFlagSet("ShareMe --headless", flag.ContinueOnError)
	ip := flags.String("ip", "", "Specific private IPv4 address to listen on")
	port := flags.Int("port", 49321, "Local listening port")
	data := flags.String("data", "", "Settings directory (required)")
	inbox := flags.String("inbox", "", "Received files directory (defaults to data/inbox)")
	statePath := flags.String("state-file", "", "Write receiver information to this local file")
	dev := flags.Bool("dev-loopback", false, "Allow 127.0.0.1 for local development only")
	control := flags.Bool("control-stdio", false, "Local development: receive approval decisions on stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *ip == "" || *data == "" {
		return errors.New("--ip and --data are required")
	}
	if *control && (!*dev || *ip != "127.0.0.1") {
		return errors.New("--control-stdio requires --dev-loopback and --ip 127.0.0.1")
	}
	if *inbox == "" {
		*inbox = filepath.Join(*data, "inbox")
	}
	service, err := transfer.New(transfer.Config{
		DataDir: *data, InboxDir: *inbox, Assets: assets,
		MaxFileBytes: 2 << 30, AllowLoopback: *dev,
		ScanFile: safety.Scan,
	})
	if err != nil {
		return err
	}
	if err := service.Start(*ip, *port); err != nil {
		return err
	}
	defer service.Stop()
	if *statePath != "" {
		state, err := json.Marshal(service.Status())
		if err != nil {
			return err
		}
		if err := os.WriteFile(*statePath, state, 0600); err != nil {
			return fmt.Errorf("write state file: %w", err)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	controlErrors := make(chan error, 1)
	if *control {
		go func() {
			input := bufio.NewScanner(os.Stdin)
			for input.Scan() {
				var decision struct {
					ID     string `json:"id"`
					Accept bool   `json:"accept"`
				}
				if err := json.Unmarshal(input.Bytes(), &decision); err != nil {
					controlErrors <- fmt.Errorf("invalid local approval command: %w", err)
					return
				}
				if err := service.Decide(decision.ID, decision.Accept); err != nil {
					controlErrors <- err
					return
				}
			}
			if err := input.Err(); err != nil {
				controlErrors <- err
			} else {
				cancel()
			}
		}()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastPending := ""
	for {
		select {
		case <-ctx.Done():
			return service.Stop()
		case err := <-controlErrors:
			return err
		case <-ticker.C:
			if status := service.Status(); !status.Running {
				return fmt.Errorf("receiver stopped: %s", status.Error)
			}
			if *control {
				pending, err := json.Marshal(struct {
					Type    string                     `json:"type"`
					Pending []transfer.PendingTransfer `json:"pending"`
				}{Type: "pending", Pending: service.Pending()})
				if err != nil {
					return err
				}
				if string(pending) != lastPending {
					if _, err := fmt.Fprintln(os.Stdout, string(pending)); err != nil {
						return err
					}
					lastPending = string(pending)
				}
			}
		}
	}
}
