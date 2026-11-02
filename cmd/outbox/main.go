// Command outbox catches what an app sends through email and text-message
// vendors' APIs: it answers them as they do, passes the emails on to an SMTP
// inbox such as Mailpit, and keeps the text messages.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/stagdhq/outbox/internal/cli"
)

// Set at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.New(version).Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "outbox:", err)
		os.Exit(1)
	}
}
