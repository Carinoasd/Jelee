package subtitles

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

// ConverterVersion is part of every derived cache key. Bump it whenever
// ToUTF8 output for the same input could change (decoder tables, BOM or
// replacement policy), so that old entries are simply never hit again.
const ConverterVersion = 1

// ErrInvalidDerivedSource reports an unusable cache key input.
var ErrInvalidDerivedSource = errors.New("subtitles: invalid derived source")

// DerivedSource identifies the exact original subtitle bytes a UTF-8 copy
// was made from. RelPath is slash separated and relative to the library
// root, so moving the library root does not invalidate the cache. Digest is
// optional (for example a SHA-256 of the file); without it, size and
// modification time stand for the content, as in the image variant cache.
type DerivedSource struct {
	LibraryID     string
	RelPath       string
	Size          int64
	ModTimeUnixNS int64
	Digest        string
}

// DerivedUTF8Key returns the hex file stem of the UTF-8 copy of src decoded
// as cs. The key covers the source identity, the source charset (detected
// or overridden) and ConverterVersion; every field is length prefixed so no
// two inputs share an encoding. The copy is stored as "<key>.<ext>" in a
// cache directory outside the library that can be deleted and rebuilt.
func DerivedUTF8Key(src DerivedSource, cs Charset) (string, error) {
	if _, ok := cs.Encoding(); !ok {
		return "", ErrUnsupportedCharset
	}
	if src.Size < 0 || src.RelPath == "" || !utf8.ValidString(src.RelPath) || strings.ContainsRune(src.RelPath, 0) ||
		strings.Contains(src.RelPath, "\\") || path.IsAbs(src.RelPath) || path.Clean(src.RelPath) != src.RelPath ||
		src.RelPath == ".." || strings.HasPrefix(src.RelPath, "../") {
		return "", ErrInvalidDerivedSource
	}
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	number := func(v int64) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(v))
		h.Write(n[:])
	}
	field("jelee-subtitle-utf8")
	number(ConverterVersion)
	field(src.LibraryID)
	field(src.RelPath)
	number(src.Size)
	number(src.ModTimeUnixNS)
	field(src.Digest)
	field(string(cs))
	return hex.EncodeToString(h.Sum(nil)), nil
}
