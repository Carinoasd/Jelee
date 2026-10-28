package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ReplacePrepared reconstructs the saved controlled recipe against a current
// ReadSource observation. Saved XML alone cannot grant an edit lineage. A caller
// still needs current library, job/lease and media execution authorization.
// No filesystem retry is inferred from an already replaced source: the durable
// worker must distinguish committed output from an unrelated external edit.
func (w *Writer) ReplacePrepared(ctx context.Context, source *Source, prepared domain.NFOWritePreparation) error {
	return w.replacePrepared(ctx, source, prepared, nativeNFOWriteOperations())
}

func (w *Writer) replacePrepared(ctx context.Context, source *Source, prepared domain.NFOWritePreparation, ops nfoWriteOperations) error {
	if w == nil || w.budget == nil || ctx == nil || source == nil || !source.ready || source.rootInfo == nil || source.parentInfo == nil || source.fileInfo == nil {
		return ErrInvalidInput
	}
	key, err := w.preparedIntentKey(ctx, source, prepared)
	if err != nil {
		return err
	}
	// Coalesce before cloning or rebuilding large documents. Each waiter checks
	// its bounded recipe/digests, but only the owner retains reconstructed bytes.
	return w.runIntent(ctx, "prepared:"+key, source, ops, func() error {
		replacement, err := w.rebuildPrepared(ctx, source, prepared)
		if err != nil {
			return err
		}
		return writeBoundNFOSource(ctx, source, replacement, prepared.Request.Backups, ops, w.budget)
	})
}

func (w *Writer) preparedIntentKey(ctx context.Context, source *Source, prepared domain.NFOWritePreparation) (string, error) {
	release, err := w.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return "", err
	}
	defer release()
	if err := domain.ValidateNFOWritePreparation(prepared); err != nil {
		return "", ErrInvalidInput
	}
	if prepared.Scope.RootGeneration < 1 {
		return "", ErrInvalidInput
	}
	if len(prepared.Scope.Source.RootPath) > 32768 || len(source.rootPath) > 32768 {
		return "", ErrInvalidInput
	}
	requestDigest, err := domain.NFOWriteRequestDigest(prepared.Request)
	if err != nil {
		return "", ErrInvalidInput
	}
	encoded, err := json.Marshal(struct {
		Scope          domain.NFOItemScope
		SavedRoot      string
		SavedRelative  string
		ObservedRoot   string
		ObservedPath   string
		SavedStamp     domain.NFOStamp
		ObservedStamp  SourceStamp
		RequestDigest  string
		ReplacementSHA [32]byte
		MaxBytes       int64
	}{prepared.Scope, prepared.Scope.Source.RootPath, prepared.Scope.Source.RelativePath, source.rootPath, source.relative, prepared.Stamp, source.stamp, requestDigest, sha256.Sum256(prepared.Replacement), source.maxBytes})
	if err != nil {
		return "", ErrInvalidInput
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (w *Writer) rebuildPrepared(ctx context.Context, source *Source, prepared domain.NFOWritePreparation) (*Document, error) {
	release, err := w.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return nil, err
	}
	defer release()
	// Validate shape and size before cloning the bounded payload. Domain checks
	// full source digest; the replacement is checked by controlled reconstruction.
	if err := domain.ValidateNFOWritePreparation(prepared); err != nil {
		return nil, ErrInvalidInput
	}
	if prepared.Scope.RootGeneration < 1 {
		return nil, ErrInvalidInput
	}
	prepared = domain.CloneNFOWritePreparation(prepared)
	stamp := source.Stamp()
	if source.rootPath != filepath.Clean(prepared.Scope.Source.RootPath) || source.relative != prepared.Scope.Source.RelativePath || source.maxBytes != prepared.Request.MaxBytes || stamp.Size != prepared.Stamp.Size || stamp.ModifiedUnixNano != prepared.Stamp.ModifiedUnixNano || stamp.SHA256 != prepared.Stamp.SHA256 || stamp.FingerprintVersion != prepared.Stamp.FingerprintVersion || !bytes.Equal(source.original, prepared.Original) {
		return nil, ErrChanged
	}
	base, err := source.Parse(ctx)
	if err != nil {
		return nil, err
	}
	expectedRoot := map[string]string{"Movie": "movie", "HomeVideo": "movie", "Series": "tvshow", "Season": "season", "Episode": "episode"}[prepared.Scope.Kind]
	if prepared.Scope.Kind == "Episode" && base.Root == "episodedetails" {
		expectedRoot = "episodedetails"
	}
	if len(base.Entries) != 1 || base.Root != expectedRoot {
		return nil, ErrInvalidInput
	}
	return rebuildPreparedEdits(ctx, base, prepared, prepared.Request.CreateMissing, true)
}

// rebuildPreparedEdits replays the saved recipe on base. The generated ID is
// read from the saved replacement, never drawn again.
func rebuildPreparedEdits(ctx context.Context, base *Document, prepared domain.NFOWritePreparation, createMissing, validateBase bool) (*Document, error) {
	rebuilt := base
	options := TextEditOptions{CreateMissing: createMissing, BOM: prepared.Request.BOM, Indent: prepared.Request.Indent}
	var err error
	for _, edit := range prepared.Request.Edits {
		rebuilt, err = rebuilt.WithTextOptions(ctx, 0, edit.Field, edit.Value, prepared.Request.MaxBytes, options)
		if err != nil {
			return nil, err
		}
	}
	if len(base.Entries[0].UniqueIDs) == 0 {
		// Read only the saved ID; never call the random generator during replay.
		frozen, err := parseOriginal(ctx, prepared.Replacement)
		if err != nil {
			return nil, err
		}
		if len(frozen.Entries) != 1 || len(frozen.Entries[0].UniqueIDs) != 1 {
			return nil, ErrInvalidInput
		}
		id := frozen.Entries[0].UniqueIDs[0]
		if id.Type != "jelee" || id.Default || !domain.ValidID(id.Value) || id.Value[14] != '4' || !bytes.ContainsAny([]byte{id.Value[19]}, "89ab") {
			return nil, ErrInvalidInput
		}
		rebuilt, err = rebuilt.EnsureIDValue(ctx, 0, id.Value, prepared.Request.MaxBytes, options)
		if err != nil {
			return nil, err
		}
	}
	if !bytes.Equal(rebuilt.original, prepared.Replacement) {
		return nil, ErrInvalidInput
	}
	documents := []*Document{rebuilt}
	if validateBase {
		documents = []*Document{base, rebuilt}
	}
	if err := validateNFOWriteDocuments(ctx, documents...); err != nil {
		return nil, err
	}
	return rebuilt, nil
}

// createRootName is the empty root a created NFO of the item kind starts from.
func createRootName(kind string) string {
	return map[string]string{"Movie": "movie", "HomeVideo": "movie", "Series": "tvshow", "Season": "season", "Episode": "episodedetails"}[kind]
}

// createBaseDocument is the document a create edits: an empty root with an XML
// declaration, no BOM. The same bytes are rebuilt for every replay.
func createBaseDocument(ctx context.Context, kind string) (*Document, error) {
	root := createRootName(kind)
	if root == "" {
		return nil, ErrInvalidInput
	}
	return parseOriginal(ctx, []byte("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>\n<"+root+">\n</"+root+">\n"))
}

// rebuildPreparedCreate reconstructs a create preparation from its saved recipe
// alone: there is no source to observe, so nothing but the recipe, the item kind
// and the saved ID decide the bytes. They must equal the saved replacement.
func (w *Writer) rebuildPreparedCreate(ctx context.Context, prepared domain.NFOWritePreparation) (*Document, error) {
	release, err := w.budget.Acquire(ctx, app.WorkCPU)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := domain.ValidateNFOWritePreparation(prepared); err != nil || !prepared.Request.Create || prepared.Scope.RootGeneration < 1 {
		return nil, ErrInvalidInput
	}
	prepared = domain.CloneNFOWritePreparation(prepared)
	base, err := createBaseDocument(ctx, prepared.Scope.Kind)
	if err != nil {
		return nil, err
	}
	return rebuildPreparedEdits(ctx, base, prepared, true, false)
}
