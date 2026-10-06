// Package migrationlock keeps released PostgreSQL migrations immutable
// (G04.2, ADR 0006). checksums.txt next to the migrations records the
// SHA-256 of every locked migration file. A locked file may never change,
// disappear or be renumbered; a new migration must be numbered above every
// locked one and recorded with `go run ./tools/migrationlock -update`, which
// only ever appends.
package migrationlock

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// FileName is the lock file inside the migrations directory.
const FileName = "checksums.txt"

const header = "# Released migration checksums (G04.2, docs/adr/0006-migration-version-policy.md).\n" +
	"# Locked files are immutable: change the schema with a new, higher-numbered migration.\n" +
	"# After adding one run `go run ./tools/migrationlock -update`; it only appends.\n"

var migrationName = regexp.MustCompile(`^([0-9]{6})_[a-z0-9_]+\.(up|down)\.sql$`)

// Entry is one locked migration file.
type Entry struct {
	Name string
	Sum  string
}

// Version is the migration number of the file.
func (e Entry) Version() int { return version(e.Name) }

func version(name string) int {
	m := migrationName.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Parse reads a lock file: comment lines and "<sha256>  <file>" lines in
// file name order.
func Parse(data []byte) ([]Entry, error) {
	var entries []Entry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		sum, name, ok := strings.Cut(text, "  ")
		if !ok || len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" || version(name) < 0 {
			return nil, fmt.Errorf("%s line %d is not \"<sha256>  <NNNNNN_name.up|down.sql>\"", FileName, line)
		}
		if n := len(entries); n > 0 && entries[n-1].Name >= name {
			return nil, fmt.Errorf("%s line %d: entries must be unique and sorted by file name", FileName, line)
		}
		entries = append(entries, Entry{Name: name, Sum: sum})
	}
	return entries, scanner.Err()
}

// Format renders entries as a lock file.
func Format(entries []Entry) []byte {
	var b bytes.Buffer
	b.WriteString(header)
	for _, e := range entries {
		b.WriteString(e.Sum + "  " + e.Name + "\n")
	}
	return b.Bytes()
}

// Sum is the SHA-256 of a migration file's bytes (.gitattributes keeps them
// LF on every platform).
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Files hashes every migration in fsys (the migrations directory) and
// checks names: NNNNNN_name.up.sql with a matching down file, one name per
// number.
func Files(fsys fs.FS) ([]Entry, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var problems []string
	stems := map[int]string{}
	var entries []Entry
	for _, name := range names {
		if version(name) < 0 {
			problems = append(problems, name+": not named NNNNNN_name.up.sql or .down.sql")
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".up.sql"), ".down.sql")
		if other, ok := stems[version(name)]; ok && other != stem {
			problems = append(problems, fmt.Sprintf("%s: migration %06d is also named %s", name, version(name), other))
		}
		stems[version(name)] = stem
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Name: name, Sum: Sum(data)})
	}
	for _, stem := range stems {
		for _, direction := range []string{".up.sql", ".down.sql"} {
			if !slices.ContainsFunc(entries, func(e Entry) bool { return e.Name == stem+direction }) {
				problems = append(problems, stem+direction+": missing (every migration has up and down)")
			}
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return entries, nil
}

// Check compares the lock with the migration files. It returns the files
// that may be appended (new, numbered above every locked migration) and the
// violations: a locked file changed, removed or renamed, and new files that
// are not locked yet or numbered at or below a locked one.
func Check(lock, files []Entry) (appendable []Entry, violations []string) {
	current := map[string]string{}
	for _, f := range files {
		current[f.Name] = f.Sum
	}
	locked := map[string]bool{}
	highest := -1
	for _, e := range lock {
		locked[e.Name] = true
		highest = max(highest, e.Version())
		switch sum, ok := current[e.Name]; {
		case !ok:
			violations = append(violations, e.Name+": locked migration is missing (released migrations may not be removed, renamed or renumbered)")
		case sum != e.Sum:
			violations = append(violations, e.Name+": locked migration changed (released migrations are immutable; add a new migration instead)")
		}
	}
	for _, f := range files {
		if locked[f.Name] {
			continue
		}
		if f.Version() <= highest {
			violations = append(violations, fmt.Sprintf("%s: new migration must be numbered above the last locked migration %06d", f.Name, highest))
			continue
		}
		appendable = append(appendable, f)
	}
	return appendable, violations
}

// Retained reports base entries (the lock of the target branch) that the
// current lock no longer holds unchanged.
func Retained(base, current []Entry) []string {
	sums := map[string]string{}
	for _, e := range current {
		sums[e.Name] = e.Sum
	}
	var violations []string
	for _, e := range base {
		if sum, ok := sums[e.Name]; !ok || sum != e.Sum {
			violations = append(violations, e.Name+": entry of the base lock was removed or changed")
		}
	}
	return violations
}
