// migrationlock is the G04.2 gate that keeps released PostgreSQL migrations
// immutable (ADR 0006). Without flags it fails when a locked migration
// changed, disappeared or was renumbered, or when a migration is not locked.
// -update appends new migrations (numbered above every locked one) to
// checksums.txt and never rewrites an entry. -base REF also requires every
// entry of REF's lock to be present unchanged, so a lock edit cannot hide a
// changed migration in a pull request. Run it from the repository root.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/platform/migrationlock"
)

const defaultDir = "internal/adapter/postgres/migrations"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrationlock", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", defaultDir, "migrations directory relative to the repository root")
	update := flags.Bool("update", false, "append new migrations to the lock")
	base := flags.String("base", "", "git revision whose lock entries must be retained unchanged")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	lockPath := filepath.Join(*dir, migrationlock.FileName)
	files, err := migrationlock.Files(os.DirFS(*dir))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "migrationlock:", err)
		return 1
	}
	data, err := os.ReadFile(lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(stderr, "migrationlock: cannot read", lockPath)
		return 2
	}
	lock, err := migrationlock.Parse(data)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "migrationlock:", err)
		return 1
	}
	appendable, violations := migrationlock.Check(lock, files)
	if *base != "" {
		baseLock, err := gitLock(*base, path.Join(filepath.ToSlash(*dir), migrationlock.FileName))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "migrationlock:", err)
			return 2
		}
		violations = append(violations, migrationlock.Retained(baseLock, lock)...)
	}
	if len(violations) > 0 {
		_, _ = fmt.Fprintln(stderr, "migrationlock: released migrations are immutable (G04.2, docs/adr/0006-migration-version-policy.md):")
		for _, v := range violations {
			_, _ = fmt.Fprintln(stderr, "  "+v)
		}
		return 1
	}
	if len(appendable) == 0 {
		_, _ = fmt.Fprintf(stdout, "migrationlock: %d migration files locked and unchanged\n", len(lock))
		return 0
	}
	if !*update {
		_, _ = fmt.Fprintln(stderr, "migrationlock: migrations not in the lock; run `go run ./tools/migrationlock -update` (make migration-lock) and commit "+lockPath+":")
		for _, e := range appendable {
			_, _ = fmt.Fprintln(stderr, "  "+e.Name)
		}
		return 1
	}
	if err := os.WriteFile(lockPath, //nolint:gosec // G703: the operator names the migrations directory of this repository
		migrationlock.Format(append(lock, appendable...)), 0o644); err != nil {
		_, _ = fmt.Fprintln(stderr, "migrationlock: cannot write", lockPath)
		return 2
	}
	_, _ = fmt.Fprintf(stdout, "migrationlock: locked %d new migration files\n", len(appendable))
	return 0
}

// gitLock reads the lock file at revision; a revision without one has
// nothing to retain.
func gitLock(revision, file string) ([]migrationlock.Entry, error) {
	if strings.HasPrefix(revision, "-") {
		return nil, errors.New("invalid base revision")
	}
	if err := exec.Command("git", "rev-parse", "--verify", "--quiet", revision+"^{commit}").Run(); err != nil {
		return nil, errors.New("base revision is not a commit in this repository")
	}
	out, err := exec.Command("git", "show", revision+":"+file).Output()
	if err != nil {
		return nil, nil
	}
	return migrationlock.Parse(out)
}
