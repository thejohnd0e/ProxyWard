// Command proxyward runs the proxy monitoring server.
//
// Usage:
//
//	proxyward                      run the server (same as "serve")
//	proxyward admin set-password   set or reset the administrator password
//	proxyward backup --output F    write a consistent backup archive (safe while running)
//	proxyward restore --input F    restore an archive into the data directory (server stopped)
//	proxyward healthcheck          exit 0 when the local server answers /healthz
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/thejohnd0e/ProxyWard/internal/app"
	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/config"
)

func main() {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := os.Args[1:]
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "serve"):
		if err := app.Run(ctx, cfg); err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	case len(args) == 2 && args[0] == "admin" && args[1] == "set-password":
		if err := setPassword(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println("Administrator password updated. All sessions were revoked.")
	case args[0] == "backup":
		fs := flag.NewFlagSet("backup", flag.ExitOnError)
		output := fs.String("output", "", "path of the archive to create (must not exist)")
		_ = fs.Parse(args[1:])
		if *output == "" {
			fmt.Fprintln(os.Stderr, "usage: proxyward backup --output <file.tar.gz>")
			os.Exit(2)
		}
		if err := app.CreateBackup(ctx, cfg, *output); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println("Backup written to", *output)
	case args[0] == "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		input := fs.String("input", "", "backup archive to restore, or - to read it from standard input")
		force := fs.Bool("force", false, "replace an existing database")
		_ = fs.Parse(args[1:])
		if *input == "" {
			fmt.Fprintln(os.Stderr, "usage: proxyward restore --input <file.tar.gz> [--force]   (stop the server first)")
			os.Exit(2)
		}
		if err := app.RestoreBackup(ctx, cfg, *input, *force); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println("Backup restored into", cfg.DataDir)
	case len(args) == 1 && args[0] == "healthcheck":
		if err := app.HealthCheck(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: proxyward [serve | admin set-password | backup --output F | restore --input F [--force] | healthcheck]")
		os.Exit(2)
	}
}

// setPassword reads the new password from a terminal prompt or, when stdin is
// piped, from its first line. The password is never a command-line argument.
func setPassword(ctx context.Context, cfg config.Config) error {
	password, err := readPassword(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	return app.SetAdminPassword(ctx, cfg, password)
}

func readPassword(in *os.File, out io.Writer) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(out, "New administrator password (at least %d characters): ", auth.MinPasswordLength)
		first, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		fmt.Fprint(out, "Repeat password: ")
		second, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if string(first) != string(second) {
			return "", errors.New("passwords do not match")
		}
		return string(first), nil
	}
	line, err := bufio.NewReader(io.LimitReader(in, auth.MaxPasswordBytes+16)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
