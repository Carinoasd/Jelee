package nfo

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// A complete observation is required; exceeding the bound is not absence.
const maxItemDirectoryEntries = 65536

var _ app.NFOItemSelectionReader = (*SummaryReader)(nil)
var _ app.NFOItemObservationReader = (*SummaryReader)(nil)

type itemCandidateObservation struct {
	selected, digest            string
	root, directory, media, nfo os.FileInfo
}

func (r *SummaryReader) SelectItemNFO(ctx context.Context, scope domain.NFOItemScope) (domain.NFOItemSelection, error) {
	observed, err := r.ObserveItemNFO(ctx, scope)
	if err != nil {
		return domain.NFOItemSelection{}, err
	}
	return observed.Selection(), nil
}

type itemNFOObservation struct {
	reader   *SummaryReader
	scope    domain.NFOItemScope
	selected domain.NFOItemSelection
	physical itemCandidateObservation
}

func (*itemNFOObservation) String() string   { return "nfo item observation (data redacted)" }
func (*itemNFOObservation) GoString() string { return "nfo item observation (data redacted)" }

func (o *itemNFOObservation) Selection() domain.NFOItemSelection {
	value := o.selected
	value.Fields.Fields = slices.Clone(value.Fields.Fields)
	value.Fields.LockedFields = slices.Clone(value.Fields.LockedFields)
	return value
}

func (o *itemNFOObservation) Recheck(ctx context.Context) (app.NFOItemObservation, error) {
	observed, err := o.reader.ObserveItemNFO(ctx, o.scope)
	if err != nil {
		if errors.Is(err, domain.ErrNFOItemAbsent) {
			return nil, domain.ErrNFOSourceChanged
		}
		return nil, err
	}
	fresh := observed.(*itemNFOObservation)
	if !os.SameFile(o.physical.root, fresh.physical.root) || !os.SameFile(o.physical.directory, fresh.physical.directory) || !sameSourceInfo(o.physical.media, fresh.physical.media) || !sameSourceInfo(o.physical.nfo, fresh.physical.nfo) {
		return nil, domain.ErrNFOSourceChanged
	}
	return fresh, nil
}

func (r *SummaryReader) ObserveItemNFO(ctx context.Context, scope domain.NFOItemScope) (app.NFOItemObservation, error) {
	if ctx == nil || !domain.ValidNFOItemScope(scope) {
		return nil, domain.ErrInvalid
	}
	if r == nil || domain.ValidateNFOIdentity(r.identity) != nil {
		return nil, domain.ErrNFOReaderUnavailable
	}
	first, err := observeItemCandidates(ctx, scope)
	if err != nil {
		return nil, err
	}
	if first.selected == "" {
		return nil, domain.ErrNFOItemAbsent
	}
	fields, err := r.ReadItemFields(ctx, domain.NFOSource{RootPath: scope.Source.RootPath, RelativePath: first.selected}, scope.Kind)
	if err != nil {
		return nil, err
	}
	last, err := observeItemCandidates(ctx, scope)
	if err != nil {
		return nil, err
	}
	if first.selected != last.selected || first.digest != last.digest || !os.SameFile(first.root, last.root) || !os.SameFile(first.directory, last.directory) || !sameSourceInfo(first.media, last.media) || !sameSourceInfo(first.nfo, last.nfo) || fields.Stamp.Size != last.nfo.Size() || fields.Stamp.ModifiedUnixNano != last.nfo.ModTime().UnixNano() {
		return nil, domain.ErrNFOSourceChanged
	}
	value := domain.NFOItemSelection{RelativePath: last.selected, CandidateDigest: last.digest, Fields: fields}
	if !domain.ValidNFOItemSelection(scope, value) {
		return nil, domain.ErrNFOReaderUnavailable
	}
	return &itemNFOObservation{reader: r, scope: scope, selected: value, physical: last}, nil
}

func observeItemCandidates(ctx context.Context, scope domain.NFOItemScope) (value itemCandidateObservation, resultErr error) {
	if err := ctx.Err(); err != nil {
		return value, err
	}
	if !filepath.IsAbs(scope.Source.RootPath) {
		return value, domain.ErrNFOInputUnavailable
	}
	rootPath := filepath.Clean(scope.Source.RootPath)
	rootMode, err := os.Lstat(rootPath)
	if err != nil || !rootMode.IsDir() || rootMode.Mode()&os.ModeSymlink != 0 {
		return value, domain.ErrNFOInputUnavailable
	}
	root, err := os.OpenRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return value, domain.ErrNFOInputUnavailable
	}
	defer func() {
		if root.Close() != nil {
			resultErr = domain.ErrNFOInputUnavailable
		}
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
	}()
	value.root, err = root.Stat(".")
	if err != nil || !value.root.IsDir() {
		return value, domain.ErrNFOInputUnavailable
	}
	// Reject symlink components, including a link that resolves within the root.
	components := strings.Split(scope.MediaPath, "/")
	for i := range components {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		info, err := root.Lstat(filepath.FromSlash(strings.Join(components[:i+1], "/")))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(components)-1 && !info.IsDir() || i == len(components)-1 && !info.Mode().IsRegular() {
			return value, domain.ErrNFOInputUnavailable
		}
	}
	media, err := root.OpenFile(filepath.FromSlash(scope.MediaPath), readOnlyFlags(), 0)
	if err != nil {
		return value, domain.ErrNFOInputUnavailable
	}
	defer func() {
		if media.Close() != nil {
			resultErr = domain.ErrNFOInputUnavailable
		}
	}()
	value.media, err = media.Stat()
	if err != nil || !validSourceInfo(value.media) {
		return value, domain.ErrNFOInputUnavailable
	}
	parent := path.Dir(scope.MediaPath)
	directory, err := root.OpenRoot(filepath.FromSlash(parent))
	if err != nil {
		return value, domain.ErrNFOInputUnavailable
	}
	defer func() {
		if directory.Close() != nil {
			resultErr = domain.ErrNFOInputUnavailable
		}
	}()
	value.directory, err = directory.Stat(".")
	if err != nil || !value.directory.IsDir() {
		return value, domain.ErrNFOInputUnavailable
	}
	listing, err := directory.Open(".")
	if err != nil {
		return value, domain.ErrNFOInputUnavailable
	}
	var closeOnce sync.Once
	var closeErr error
	closeListing := func() { closeOnce.Do(func() { closeErr = listing.Close() }) }
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(cancelled); closeListing() })
	defer func() {
		if !stop() {
			<-cancelled
		}
		closeListing()
		if closeErr != nil {
			resultErr = domain.ErrNFOInputUnavailable
		}
	}()
	candidates := domain.NFOItemCandidatePaths(scope)
	matches := make([][]string, len(candidates))
	names := []string{}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		entries, err := listing.ReadDir(256)
		count += len(entries)
		if count > maxItemDirectoryEntries {
			return value, domain.ErrNFOSourceLimit
		}
		for _, entry := range entries {
			for i, candidate := range candidates {
				if !strings.EqualFold(entry.Name(), path.Base(candidate)) {
					continue
				}
				info, statErr := directory.Lstat(entry.Name())
				if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
					return value, domain.ErrNFOInputUnavailable
				}
				matches[i] = append(matches[i], path.Join(parent, entry.Name()))
				names = append(names, path.Join(parent, entry.Name()))
				if len(matches[i]) > 1 {
					return value, domain.ErrNFOInputUnavailable
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return value, domain.ErrNFOInputUnavailable
		}
	}
	for _, match := range matches {
		if len(match) == 1 && value.selected == "" {
			value.selected = match[0]
		}
	}
	value.digest = domain.NFOCandidateDigest(names)
	if value.selected != "" {
		value.nfo, err = root.Stat(filepath.FromSlash(value.selected))
		if err != nil || !validSourceInfo(value.nfo) {
			return value, domain.ErrNFOSourceChanged
		}
	}
	afterDir, err := directory.Stat(".")
	if err != nil || !os.SameFile(value.directory, afterDir) || !value.directory.ModTime().Equal(afterDir.ModTime()) {
		return value, domain.ErrNFOSourceChanged
	}
	afterMedia, err := root.Stat(filepath.FromSlash(scope.MediaPath))
	if err != nil || !sameSourceInfo(value.media, afterMedia) {
		return value, domain.ErrNFOSourceChanged
	}
	currentRoot, err := os.OpenRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return value, domain.ErrNFOSourceChanged
	}
	defer func() {
		if currentRoot.Close() != nil {
			resultErr = domain.ErrNFOInputUnavailable
		}
	}()
	currentInfo, err := currentRoot.Stat(".")
	if err != nil || !os.SameFile(value.root, currentInfo) {
		return value, domain.ErrNFOSourceChanged
	}
	currentDir, err := currentRoot.Stat(filepath.FromSlash(parent))
	if err != nil || !os.SameFile(value.directory, currentDir) {
		return value, domain.ErrNFOSourceChanged
	}
	return value, nil
}
