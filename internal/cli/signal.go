package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Instead of import kubernetes package, I only use the standard library.
// This is useful for some of the components who do not add kubernetes as a dependency, making the image smaller.

var onlyOneSignalHandler = make(chan struct{})
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT}

// SetupSignalHandler registered for SIGTERM and SIGINT. A context is returned
// which is cancelled on one of these signals or optionally a custom cancellation channel.
// If a second signal is caught, the program is terminated with exit code 1.
// Usage: To programmatically cancel the context with custom cancel channel
// customCtx, customCancel := context.WithCancel(context.Background())
// ctxWithCustomCancel := SetupSignalHandler(customCtx)
// To programmatically cancel the context with custom cancel context
// customCancel()
func SetupSignalHandler(customCtx ...context.Context) context.Context {
	close(onlyOneSignalHandler) // panics when called twice

	c := make(chan os.Signal, 2)
	ctx, cancel := context.WithCancel(context.Background())
	signal.Notify(c, shutdownSignals...)

	go func() {
		var customCancel <-chan struct{}
		if len(customCtx) > 0 {
			customCancel = customCtx[0].Done()
		}

		for {
			select {
			case sig := <-c:
				cancel()
				if sig != nil { // Check for second signal
					os.Exit(1) // Exit directly after receiving any signal.
				}
			case <-customCancel:
				cancel() // Cancel the context when customCtx is done
			}
		}
	}()

	return ctx
}
