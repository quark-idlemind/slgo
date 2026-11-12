//go:build !unix

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
)

// Where a process cannot send itself SIGTERM, the daemon is stopped by
// canceling the context main waits on, which is what the signal does.
var (
	stopMu   sync.Mutex
	stopLast context.CancelFunc
)

func init() {
	signalContext = func(parent context.Context, sig ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := signal.NotifyContext(parent, sig...)
		stopMu.Lock()
		stopLast = cancel
		stopMu.Unlock()
		return ctx, cancel
	}
}

// stopTheDaemon cancels the context of the daemon started last.
func stopTheDaemon() error {
	stopMu.Lock()
	cancel := stopLast
	stopMu.Unlock()
	if cancel == nil {
		return errors.New("the daemon has not made its context yet")
	}
	cancel()
	return nil
}
