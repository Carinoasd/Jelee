package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func runInventoryImport(ctx context.Context, args []string, out, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("import-inventory", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	job := flags.String("job", "", "completed scan job ID")
	entry := flags.String("entry", "", "inventory entry ID")
	title := flags.String("title", "", "catalog title")
	kind := flags.String("kind", "HomeVideo", "HomeVideo, Movie or Episode")
	parent := flags.String("parent", "", "optional Episode parent item ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !domain.ValidID(*job) || !domain.ValidID(*entry) || *title == "" || len(*title) > 1024 || !domain.ValidVideoItemKind(*kind) || *parent != "" && (*kind != "Episode" || !domain.ValidID(*parent)) {
		fmt.Fprintln(diagnostic, "supply valid job and entry IDs, title, video kind and optional Episode parent")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(diagnostic, "configuration unavailable")
		return 1
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
	if err != nil {
		fmt.Fprintln(diagnostic, "database unavailable or migration required")
		return 1
	}
	defer store.Pool.Close()
	source, err := store.ResolveInventoryImport(ctx, *job, *entry)
	if err != nil {
		fmt.Fprintln(diagnostic, "candidate unavailable: use the latest accepted scan")
		return 1
	}
	if !validInventoryImportFile(ctx, source) {
		fmt.Fprintln(diagnostic, "candidate file changed or is unavailable; scan again")
		return 1
	}
	id, err := store.ImportInventoryVideo(ctx, source, *title, *kind, *parent)
	if err != nil {
		fmt.Fprintln(diagnostic, "import rejected: check duplicate location, parent and scan state")
		return 1
	}
	fmt.Fprintf(out, "Registered source %s; original file unchanged.\n", id)
	return 0
}

func validInventoryImportFile(ctx context.Context, source domain.InventoryImportSource) bool {
	return scan.New().VerifyInventoryImport(ctx, source) == nil
}
