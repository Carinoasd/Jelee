package ignoresource

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

type ScanEntry struct {
	Path             string       `json:"-"`
	Directory        bool         `json:"-"`
	Size             int64        `json:"-"`
	ModifiedUnixNano int64        `json:"-"`
	Match            ignore.Match `json:"-"`
}

func (ScanEntry) String() string   { return "ignore scan entry (data redacted)" }
func (ScanEntry) GoString() string { return "ignore scan entry (data redacted)" }

type ScanBatch struct {
	Proofs  []DirectoryProof `json:"-"`
	Entries []ScanEntry      `json:"-"`
	Skipped int64            `json:"-"`
	Done    bool             `json:"-"`
}

func (ScanBatch) String() string   { return "ignore scan batch (data redacted)" }
func (ScanBatch) GoString() string { return "ignore scan batch (data redacted)" }

// ScanDirectory compiles the reachable rule chain once per directory and
// enumerates from the same held handle that supplies its final directory proof.
// Intermediate batches are provisional. Done is emitted only after sources
// have been re-opened/re-hashed and all owned handles successfully closed.
// The repository must still compare retained proofs and verify the whole scan.
func (r *Resolver) ScanDirectory(ctx context.Context, root, relative string, options ignore.Options, emit func(ScanBatch) error) (resultErr error) {
	if ctx == nil || r == nil || r.slots == nil || emit == nil || options.Case > ignore.CaseASCIIInsensitive {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRoot(root); err != nil {
		return err
	}
	if relative != "." {
		if err := validateCandidate(relative); err != nil {
			return err
		}
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, MaxDuration)
	defer cancel()
	var owned []directory
	closeAll := func() error {
		var err error
		for i := len(owned) - 1; i >= 0; i-- {
			if owned[i].Close() != nil {
				err = ErrRead
			}
		}
		owned = nil
		return err
	}
	callbackFailure := false
	defer func() {
		err := closeAll()
		if !callbackFailure {
			if ctx.Err() != nil {
				resultErr = ctx.Err()
			} else if err != nil {
				resultErr = err
			}
			if resultErr != nil {
				resultErr = safeError(resultErr)
			}
		}
	}()
	current, err := openNativeRoot(root)
	if err != nil {
		return err
	}
	owned = append(owned, current)
	var components []string
	if relative != "." {
		components = strings.Split(relative, "/")
	}
	var chain []directoryObservation
	var sources []ignore.Source
	var program *ignore.Program
	remaining := ignore.MaxTotalSourceBytes
	compileBytes := 0
	parent := ""
	for i := 0; i <= len(components); i++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		state, err := current.Stat()
		if err != nil {
			return err
		}
		if state.kind != nodeDirectory {
			return ErrUnsafe
		}
		stamp, raw, err := readRule(ctx, current, &remaining)
		if err != nil {
			return err
		}
		chain = append(chain, directoryObservation{parent, state.identity, stamp})
		sourcePath := ".jeleeignore"
		if parent != "" {
			sourcePath = parent + "/.jeleeignore"
		}
		if err = validateCandidate(sourcePath); err != nil {
			return err
		}
		if stamp.present {
			sources = append(sources, ignore.Source{Path: sourcePath, Text: raw})
		}
		if program == nil || stamp.present {
			compileBytes += ignore.MaxTotalSourceBytes - remaining
			if compileBytes > MaxCompileInputBytes {
				return ErrWorkLimit
			}
			program, err = ignore.Compile(ctx, sources, options)
			if err != nil {
				return err
			}
		}
		if i == len(components) {
			break
		}
		prefix := components[i]
		if parent != "" {
			prefix = parent + "/" + prefix
		}
		match, err := program.Evaluate(ctx, prefix, ignore.Directory)
		if err != nil {
			return err
		}
		if match.Outcome == ignore.Exclude {
			return ErrChanged
		}
		current, err = current.OpenDirectory(components[i])
		if err != nil {
			return err
		}
		owned = append(owned, current)
		parent = prefix
	}
	reader, ok := current.(scanDirectory)
	if !ok {
		return ErrUnavailable
	}
	// Cancellation closes precisely the handle doing ReadDir. Ancestors remain
	// owned until this invocation returns; no goroutine mutates the owned slice.
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(joined); _ = reader.Close() })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	var skipped int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		entries, readErr := reader.ReadEntries()
		if err = ctx.Err(); err != nil {
			return err
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		batch := ScanBatch{Proofs: (Observation{chain: chain}).DirectoryProofs()}
		for _, entry := range entries {
			if !validScanName(entry.name) {
				skipped++
				continue
			}
			name := entry.name
			if parent != "" {
				name = parent + "/" + name
			}
			if err = validateCandidate(name); err != nil {
				return err
			}
			kind := ignore.File
			if entry.state.kind == nodeDirectory {
				kind = ignore.Directory
			} else if entry.state.kind != nodeRegular {
				skipped++
				continue
			}
			match, err := program.Evaluate(ctx, name, kind)
			if err != nil {
				return err
			}
			batch.Entries = append(batch.Entries, ScanEntry{Path: name, Directory: kind == ignore.Directory, Size: entry.state.size, ModifiedUnixNano: entry.state.modifiedUnixNano, Match: match})
		}
		if len(batch.Entries) > 0 {
			if err = emit(batch); err != nil {
				callbackFailure = true
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if err = verifyScanSources(ctx, root, components, chain); err != nil {
		return err
	}
	if err = closeAll(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = emit(ScanBatch{Proofs: (Observation{chain: chain}).DirectoryProofs(), Skipped: skipped, Done: true})
	if err != nil {
		callbackFailure = true
		return err
	}
	return ctx.Err()
}

func validScanName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:") && validateCandidate(name) == nil
}

func verifyScanSources(ctx context.Context, root string, components []string, chain []directoryObservation) (resultErr error) {
	var owned []directory
	defer func() {
		for i := len(owned) - 1; i >= 0; i-- {
			if owned[i].Close() != nil {
				resultErr = ErrRead
			}
		}
	}()
	current, err := openNativeRoot(root)
	if err != nil {
		return err
	}
	owned = append(owned, current)
	remaining := ignore.MaxTotalSourceBytes
	for i, previous := range chain {
		if err = ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			current, err = current.OpenDirectory(components[i-1])
			if err != nil {
				return err
			}
			owned = append(owned, current)
		}
		state, err := current.Stat()
		if err != nil {
			return err
		}
		if state.kind != nodeDirectory || state.identity != previous.identity {
			return ErrChanged
		}
		stamp, _, err := readRule(ctx, current, &remaining)
		if err != nil {
			return err
		}
		if stamp != previous.source {
			return ErrChanged
		}
	}
	return ctx.Err()
}
