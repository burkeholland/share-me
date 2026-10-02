package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"shareme/internal/peer"
)

func TestReconnectDoesNotEmitTransferError(t *testing.T) {
	// A context without Wails bindings is safe only when emission is skipped.
	app := &App{ctx: context.Background()}
	app.notifyError(fmt.Errorf("%w: %w", peer.ErrReconnecting, errors.New("test outage")))
}
