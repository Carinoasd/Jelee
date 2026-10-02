package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
)

// Absence is proven by a complete bounded directory observation beneath an
// authorized root. It is distinct from an unavailable or unsafe input.
var ErrNFOItemAbsent = errors.New("nfo_item_absent")

// NFOItemSelection is internal reader state. Paths never enter HTTP or audit.
type NFOItemSelection struct {
	RelativePath    string
	CandidateDigest string
	Fields          NFOItemFields
}

func (NFOItemSelection) String() string   { return "nfo item selection (paths redacted)" }
func (NFOItemSelection) GoString() string { return "nfo item selection (paths redacted)" }

// A specific video basename takes priority over the conventional directory NFO.
func NFOItemCandidatePaths(scope NFOItemScope) []string {
	if !ValidNFOItemScope(scope) {
		return nil
	}
	generic := "movie.nfo"
	if scope.Kind == "Series" {
		generic = "tvshow.nfo"
	}
	paths := []string{scope.Source.RelativePath}
	second := path.Join(path.Dir(scope.MediaPath), generic)
	if !strings.EqualFold(paths[0], second) {
		paths = append(paths, second)
	}
	return paths
}

func NFOCandidateDigest(names []string) string {
	ordered := slices.Clone(names)
	slices.Sort(ordered)
	encoded, _ := json.Marshal(ordered)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func ValidNFOItemSelection(scope NFOItemScope, selected NFOItemSelection) bool {
	if !ValidNFOItemFields(selected.Fields) || !(selected.Fields.Kind == scope.Kind || scope.Kind == "HomeVideo" && selected.Fields.Kind == "Movie") || !probeHex(selected.CandidateDigest, 64) || len(selected.RelativePath) > 1024 || path.Clean(selected.RelativePath) != selected.RelativePath || path.Dir(selected.RelativePath) != path.Dir(scope.MediaPath) {
		return false
	}
	for _, candidate := range NFOItemCandidatePaths(scope) {
		if strings.EqualFold(path.Base(candidate), path.Base(selected.RelativePath)) {
			return true
		}
	}
	return false
}
