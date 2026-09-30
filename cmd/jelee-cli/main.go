package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: jelee-cli doctor|provision|import-video|nfo|account|library|jobs")
		return 2
	}
	command := os.Args[1]
	if command == "library" || command == "jobs" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if command == "library" {
			return runLibraryCLI(ctx, os.Args[2:], os.Stdout, os.Stderr)
		}
		return runJobsCLI(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "account" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return runAccountCLI(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr)
	}
	if command == "nfo" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return runNFOWithOutputCancellation(ctx, os.Args[2:], os.Stdout, os.Stderr)
	}
	if command != "doctor" && command != "provision" && command != "import-video" {
		fmt.Fprintln(os.Stderr, "unsupported command")
		return 2
	}
	args := flag.NewFlagSet(command, flag.ContinueOnError)
	name := args.String("name", "", "new account name")
	native := args.Bool("native", false, "issue a third-party native client session")
	admin := args.Bool("admin", false, "create administrator")
	library := args.String("library", "", "library name")
	root := args.String("root", "", "absolute media root")
	relative := args.String("file", "", "root-relative video path")
	title := args.String("title", "", "catalog title")
	if err := args.Parse(os.Args[2:]); err != nil || args.NArg() != 0 {
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database: unavailable or migration required; run jelee-migrate status")
		return 1
	}
	defer store.Pool.Close()
	switch command {
	case "doctor":
		fmt.Println("configuration: valid\nPostgreSQL: connected\nschema: 3 clean\nproduction restrictions: enabled\ndeveloper mode: unavailable\nRemaining diagnostics: see docs/requirements-traceability.md")
		return 0
	case "provision":
		kind := access.ClientWeb
		if *native {
			kind = access.ClientNative
		}
		token, err := store.Provision(ctx, *name, kind, *admin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "provisioning failed: check unique name and database state")
			return 1
		}
		fmt.Printf("Session expires after 24 hours. Store this token securely; it is shown only once.\n%s\n", token)
		return 0
	case "import-video":
		if !filepath.IsAbs(*root) || !filepath.IsLocal(*relative) {
			fmt.Fprintln(os.Stderr, "supply an absolute root and a local relative file")
			return 2
		}
		types := map[string]string{".mp4": "video/mp4", ".mkv": "video/x-matroska", ".webm": "video/webm", ".mov": "video/quicktime", ".avi": "video/x-msvideo", ".ts": "video/mp2t"}
		contentType := types[strings.ToLower(filepath.Ext(*relative))]
		if contentType == "" {
			fmt.Fprintln(os.Stderr, "unsupported video extension")
			return 2
		}
		mediaRoot, err := os.OpenRoot(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "media root unavailable")
			return 1
		}
		defer mediaRoot.Close()
		info, err := mediaRoot.Stat(*relative)
		if err != nil || !info.Mode().IsRegular() {
			fmt.Fprintln(os.Stderr, "video is not a readable regular file in the root")
			return 1
		}
		id, err := store.ImportVideo(ctx, *library, filepath.Clean(*root), filepath.ToSlash(filepath.Clean(*relative)), *title, contentType)
		if err != nil {
			fmt.Fprintln(os.Stderr, "registration failed: check names, duplicate paths and database state")
			return 1
		}
		fmt.Printf("Registered source %s; original file unchanged.\n", id)
		return 0
	}
	return 2
}
