package common

import (
	"context"
	"sync"
)

var (
	processCtx, processCancel = context.WithCancel(context.Background())
	processShutdownOnce       sync.Once
)

// ProcessContext is cancelled when the process begins a graceful shutdown.
// Background work that is not tied to an HTTP request should derive from it so
// it stops promptly instead of racing a closing database.
func ProcessContext() context.Context {
	return processCtx
}

// TriggerProcessShutdown cancels ProcessContext. It is idempotent and safe to
// call from multiple shutdown paths.
func TriggerProcessShutdown() {
	processShutdownOnce.Do(processCancel)
}
