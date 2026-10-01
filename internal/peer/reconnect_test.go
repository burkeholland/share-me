package peer

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestReconnectReportsSentinelAndPreservesCause(t *testing.T) {
	engine, _ := testEngine(t)
	reports := make(chan error, 4)
	engine.cfg.OnError = func(err error) { reports <- err }
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reports:
		var cause *net.OpError
		if !errors.Is(err, ErrReconnecting) || !errors.As(err, &cause) {
			t.Fatal("reconnect lost its classification or dial cause", err)
		}
		connected, message := engine.Status()
		if connected || !strings.Contains(message, "Retrying in 1s.") || strings.Contains(strings.ToLower(message), "signaling") {
			t.Fatalf("reconnect status=%q connected=%v", message, connected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connection failure was not reported")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}
