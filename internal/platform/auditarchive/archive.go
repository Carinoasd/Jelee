// Package auditarchive writes the audit rows a retention purge is about to
// delete to dated NDJSON+gzip files (G36.3, G46.9) and keeps those files within
// their own retention and size cap. Every access goes through an os.Root on
// the archive directory, so no name can leave it.
package auditarchive

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	filePrefix = "audit-"
	fileSuffix = ".ndjson.gz"
	tempPrefix = ".tmp-"
	// staleTemp is how long a leftover temporary file survives before the
	// retention pass removes it; a live writer finishes in seconds.
	staleTemp = 24 * time.Hour
	// MaxRetentionDays bounds the age options.
	MaxRetentionDays = 36500
	// MaxCapMB bounds the size cap in MiB.
	MaxCapMB = 1 << 20
)

var fileName = regexp.MustCompile(`^audit-(audit|security)-(\d{8})-(\d{1,18})-(\d{1,18})\.ndjson\.gz$`)

// ErrInvalid reports a bad option or a name outside the archive's own format.
var ErrInvalid = errors.New("invalid audit archive")

// Options configures an Archive. Zero retention or cap means unlimited.
type Options struct {
	// Dir is the archive directory; it is created (0700) when missing.
	Dir string
	// RetentionDays bounds the age of audit-category files and, unless
	// SecurityRetentionDays is set, of security files too.
	RetentionDays         int
	SecurityRetentionDays int
	// MaxBytes caps the total size; the oldest files go first.
	MaxBytes int64
}

// Archive implements domain.AuditArchiveSink over a directory.
type Archive struct {
	root *os.Root
	opts Options
	mu   sync.Mutex
}

// Open validates opts and opens the directory.
func Open(opts Options) (*Archive, error) {
	if opts.Dir == "" || opts.RetentionDays < 0 || opts.RetentionDays > MaxRetentionDays ||
		opts.SecurityRetentionDays < 0 || opts.SecurityRetentionDays > MaxRetentionDays || opts.MaxBytes < 0 || opts.MaxBytes > MaxCapMB<<20 {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit archive directory: %w", err)
	}
	root, err := os.OpenRoot(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("audit archive directory: %w", err)
	}
	return &Archive{root: root, opts: opts}, nil
}

// Close releases the directory handle.
func (a *Archive) Close() error { return a.root.Close() }

type group struct {
	category string
	day      string
	records  []domain.AuditRecord
}

// Archive writes one file per category and UTC day present in records. Each
// file is written to a temporary name (0600, exclusive), synced, then linked to
// its final name, which never replaces an existing file: an existing file with
// the same name must already verify, in which case the earlier attempt is kept.
// The directory is synced last, so a nil return means every file is durable.
func (a *Archive) Archive(ctx context.Context, records []domain.AuditRecord) (domain.AuditArchiveReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var receipt domain.AuditArchiveReceipt
	for _, g := range split(records) {
		if err := ctx.Err(); err != nil {
			return receipt, err
		}
		written, size, err := a.writeGroup(g)
		if err != nil {
			return receipt, err
		}
		if written {
			receipt.Files++
			receipt.Bytes += size
		}
	}
	if err := syncDir(a.root); err != nil {
		return receipt, fmt.Errorf("audit archive directory sync: %w", err)
	}
	return receipt, nil
}

func split(records []domain.AuditRecord) []group {
	var groups []group
	index := map[string]int{}
	for _, r := range records {
		key := r.Category + "/" + r.OccurredAt.UTC().Format("20060102")
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, group{category: r.Category, day: r.OccurredAt.UTC().Format("20060102")})
		}
		groups[i].records = append(groups[i].records, r)
	}
	return groups
}

func (g group) name() string {
	return filePrefix + g.category + "-" + g.day + "-" + strconv.FormatInt(g.records[0].ID, 10) + "-" + strconv.FormatInt(g.records[len(g.records)-1].ID, 10) + fileSuffix
}

func (a *Archive) writeGroup(g group) (bool, int64, error) {
	for _, r := range g.records {
		if r.Category != domain.AuditCategoryAudit && r.Category != domain.AuditCategorySecurity {
			return false, 0, ErrInvalid
		}
	}
	name := g.name()
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return false, 0, err
	}
	temp := tempPrefix + hex.EncodeToString(token[:])
	file, err := a.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, 0, fmt.Errorf("audit archive write: %w", err)
	}
	cleanup := func() { _ = a.root.Remove(temp) }
	zw := gzip.NewWriter(file)
	for _, r := range g.records {
		line, err := encodeRecord(r)
		if err == nil {
			_, err = zw.Write(line)
		}
		if err != nil {
			_ = file.Close()
			cleanup()
			return false, 0, fmt.Errorf("audit archive write: %w", err)
		}
	}
	if err = zw.Close(); err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return false, 0, fmt.Errorf("audit archive write: %w", err)
	}
	info, err := a.root.Stat(temp)
	if err != nil {
		cleanup()
		return false, 0, fmt.Errorf("audit archive write: %w", err)
	}
	// Link fails if name exists, so an archive file is never overwritten.
	err = a.root.Link(temp, name)
	cleanup()
	switch {
	case err == nil:
		return true, info.Size(), nil
	case errors.Is(err, fs.ErrExist):
		if res := verifyFile(a.root, name); res.Err != "" || res.Rows != len(g.records) {
			return false, 0, errors.New("audit archive write: an existing file with the same name does not verify")
		}
		return false, 0, nil
	default:
		return false, 0, fmt.Errorf("audit archive write: %w", err)
	}
}

// encodeRecord renders one record as an NDJSON line, in the shape of the
// administrator audit listing (domain.AuditRecord).
func encodeRecord(r domain.AuditRecord) ([]byte, error) {
	if len(r.Before) == 0 {
		r.Before = json.RawMessage("null")
	}
	if len(r.After) == 0 {
		r.After = json.RawMessage("null")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// File describes one archive file.
type File struct {
	Name     string    `json:"name"`
	Category string    `json:"category"`
	Day      string    `json:"day"`
	FirstID  int64     `json:"firstId"`
	LastID   int64     `json:"lastId"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

func parseName(name string) (File, bool) {
	m := fileName.FindStringSubmatch(name)
	if m == nil {
		return File{}, false
	}
	if _, err := time.Parse("20060102", m[2]); err != nil {
		return File{}, false
	}
	first, err1 := strconv.ParseInt(m[3], 10, 64)
	last, err2 := strconv.ParseInt(m[4], 10, 64)
	if err1 != nil || err2 != nil || first < 1 || last < first {
		return File{}, false
	}
	return File{Name: name, Category: m[1], Day: m[2], FirstID: first, LastID: last}, true
}

// List returns the archive files, oldest first.
func (a *Archive) List() ([]File, error) { return list(a.root) }

// ListDir lists the archive files of dir without creating it.
func ListDir(dir string) ([]File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return list(root)
}

func list(root *os.Root) ([]File, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	files := []File{}
	for _, e := range entries {
		f, ok := parseName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f.Bytes, f.Modified = info.Size(), info.ModTime().UTC()
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Day != files[j].Day {
			return files[i].Day < files[j].Day
		}
		if files[i].FirstID != files[j].FirstID {
			return files[i].FirstID < files[j].FirstID
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

// Prune removes archive files past their own retention and then the oldest
// files while the total exceeds the cap, plus abandoned temporary files. It
// touches only this directory; the database is never involved. A file counts
// as expired once the end of its day is RetentionDays old.
func (a *Archive) Prune(ctx context.Context, now time.Time) (domain.AuditArchivePrune, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out domain.AuditArchivePrune
	files, err := list(a.root)
	if err != nil {
		return out, fmt.Errorf("audit archive prune: %w", err)
	}
	var errs []error
	remove := func(f File) bool {
		if err := a.root.Remove(f.Name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			return false
		}
		out.Files++
		out.Bytes += f.Bytes
		return true
	}
	kept := files[:0:0]
	var total int64
	for _, f := range files {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		days := a.opts.RetentionDays
		if f.Category == domain.AuditCategorySecurity && a.opts.SecurityRetentionDays > 0 {
			days = a.opts.SecurityRetentionDays
		}
		day, _ := time.Parse("20060102", f.Day)
		if days > 0 && day.AddDate(0, 0, 1+days).Before(now) && remove(f) {
			continue
		}
		kept = append(kept, f)
		total += f.Bytes
	}
	for _, f := range kept {
		if a.opts.MaxBytes <= 0 || total <= a.opts.MaxBytes {
			break
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if remove(f) {
			total -= f.Bytes
		}
	}
	a.removeStaleTemp(now)
	if len(errs) > 0 {
		return out, fmt.Errorf("audit archive prune: %w", errors.Join(errs...))
	}
	return out, nil
}

func (a *Archive) removeStaleTemp(now time.Time) {
	dir, err := a.root.Open(".")
	if err != nil {
		return
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return
	}
	for _, e := range entries {
		if len(e.Name()) < len(tempPrefix) || e.Name()[:len(tempPrefix)] != tempPrefix || !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleTemp {
			_ = a.root.Remove(e.Name())
		}
	}
}

func syncDir(root *os.Root) error {
	if runtime.GOOS == "windows" {
		// Windows cannot fsync a directory handle; file syncs above already
		// reached the disk and NTFS journals the link.
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	if cerr := dir.Close(); err == nil {
		err = cerr
	}
	return err
}
