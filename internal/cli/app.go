// Package cli defines the outbox command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/stagdhq/outbox/internal/outbox"
	"github.com/stagdhq/outbox/internal/telemetry"
)

// New returns the root command.
func New(version string) *cli.Command {
	return &cli.Command{
		Name:    "outbox",
		Usage:   "catch what an app sends through email and text-message vendors' APIs",
		Version: version,
		Commands: []*cli.Command{
			serveCommand(version),
			healthCommand(),
			versionCommand(version),
		},
	}
}

// listenFlags are where outbox answers, for serve and health.
func listenFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "listen",
			Usage:   "where the vendors with JSON APIs (Resend) are answered, under /<vendor>, and /texts",
			Value:   ":8080",
			Sources: cli.EnvVars("OUTBOX_LISTEN"),
		},
		&cli.StringFlag{
			Name:    "aws-listen",
			Usage:   "where Amazon SES and SNS are answered, at the root as the AWS SDKs want",
			Value:   ":4566",
			Sources: cli.EnvVars("OUTBOX_AWS_LISTEN"),
		},
	}
}

func serveCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "answer the vendors' APIs, passing emails on to an SMTP inbox and keeping text messages",
		Description: "Stands in for the vendors an app sends email and text messages through, so nothing\n" +
			"leaves: emails go on to an SMTP server such as Mailpit, text messages are kept\n" +
			"(GET /texts). Point the vendors' SDKs at it:\n\n" +
			"  Resend      RESEND_BASE_URL=http://localhost:8080/resend (RESEND_API_URL in Python)\n" +
			"  Amazon SES  AWS_ENDPOINT_URL_SES=http://localhost:4566 (and _SESV2)\n" +
			"  Amazon SNS  AWS_ENDPOINT_URL_SNS=http://localhost:4566 (text messages)\n\n" +
			"  outbox serve --smtp localhost:1025",
		Flags: append(listenFlags(),
			&cli.StringFlag{
				Name:    "smtp",
				Usage:   "the SMTP server emails go on to, host:port (Mailpit's is :1025); empty only logs them",
				Sources: cli.EnvVars("OUTBOX_SMTP"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "text or json",
				Value:   "text",
				Sources: cli.EnvVars("OUTBOX_LOG_FORMAT"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "debug, info, warn or error",
				Value:   "info",
				Sources: cli.EnvVars("OUTBOX_LOG_LEVEL"),
			},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log, err := logger(cmd)
			if err != nil {
				return err
			}

			stop, err := telemetry.Setup(ctx, version)
			if err != nil {
				return fmt.Errorf("opentelemetry: %w", err)
			}

			defer func() {
				flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				if err := stop(flush); err != nil {
					log.Warn("outbox: opentelemetry", "err", err)
				}
			}()

			if telemetry.Enabled() {
				log.Info("outbox: opentelemetry on")
			}

			s := &outbox.Server{SMTP: cmd.String("smtp"), Log: log}

			servers := []*http.Server{
				{Addr: cmd.String("listen"), Handler: telemetry.Handler(s.Handler()), ReadHeaderTimeout: 10 * time.Second},
				{Addr: cmd.String("aws-listen"), Handler: telemetry.Handler(s.AWSHandler()), ReadHeaderTimeout: 10 * time.Second},
			}

			failed := make(chan error, len(servers))

			for _, srv := range servers {
				listener, err := net.Listen("tcp", srv.Addr)
				if err != nil {
					return fmt.Errorf("listening on %s: %w", srv.Addr, err)
				}

				go func() {
					if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
						failed <- err
					}
				}()
			}

			log.Info("outbox: answering", "version", version, "vendors", cmd.String("listen"), "aws", cmd.String("aws-listen"), "smtp", cmd.String("smtp"))

			select {
			case <-ctx.Done():
			case err := <-failed:
				return err
			}

			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			for _, srv := range servers {
				_ = srv.Shutdown(shutdown)
			}

			return nil
		},
	}
}

func logger(cmd *cli.Command) (*slog.Logger, error) {
	var level slog.Level

	if err := level.UnmarshalText([]byte(cmd.String("log-level"))); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}

	options := &slog.HandlerOptions{Level: level}

	switch cmd.String("log-format") {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, options)), nil

	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, options)), nil
	}

	return nil, fmt.Errorf("--log-format: text or json, not %q", cmd.String("log-format"))
}

// healthCommand asks the outbox serving at --listen and --aws-listen
// whether both answer, for a container's health check.
func healthCommand() *cli.Command {
	return &cli.Command{
		Name:  "health",
		Usage: "exit 0 when outbox answers on both its ports, for a container's health check",
		Flags: listenFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			for _, flag := range []string{"listen", "aws-listen"} {
				if err := healthy(ctx, cmd.String(flag)); err != nil {
					return fmt.Errorf("--%s: %w", flag, err)
				}
			}

			return nil
		},
	}
}

// healthy asks one port for /healthz.
func healthy(ctx context.Context, listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}

	// Listening on every address, outbox answers on loopback.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("not serving: %w", err)
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", strings.TrimSpace(string(body)))
	}

	return nil
}

func versionCommand(version string) *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "print outbox's version",
		Action: func(_ context.Context, cmd *cli.Command) error {
			_, err := fmt.Fprintf(cmd.Root().Writer, "outbox %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)

			return err
		},
	}
}
