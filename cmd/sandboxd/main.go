// Command sandboxd is the daemon that runs inside every agent sandbox.
//
// In M0 it only reports its version and waits for a termination signal so it
// can already serve as a container entrypoint. The tunnel arrives in M3.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gempir/agenthold/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("sandboxd %s (%s)\n", version.Version, version.Commit)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("sandboxd started", "version", version.Version)
	<-ctx.Done()
	logger.Info("sandboxd stopping")
}
