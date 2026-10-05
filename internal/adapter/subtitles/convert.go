package subtitles

import (
	"context"
	"errors"
	"io"
	"unicode/utf8"

	"golang.org/x/text/transform"
)

const (
	// DefaultConvertLimit applies when ToUTF8 gets a non-positive limit. ASS
	// files with embedded fonts are the largest text subtitles in practice.
	DefaultConvertLimit int64 = 64 << 20
	// MaxConvertLimit caps any requested conversion limit.
	MaxConvertLimit int64 = 256 << 20
)

// ConvertResult reports one conversion.
type ConvertResult struct {
	Read    int64
	Written int64
	// Replaced counts U+FFFD written for invalid or truncated sequences: one
	// per offending byte for UTF-8 sources, one per undecodable sequence for
	// the others. For non-UTF-8 sources it also counts a U+FFFD that the
	// source encoded on purpose, which only GB18030 and UTF-16 can express.
	Replaced int64
	// BOMStripped reports that one leading U+FEFF was dropped.
	BOMStripped bool
}

// ToUTF8 decodes r from cs and streams UTF-8 to w. At most limit input bytes
// are accepted (DefaultConvertLimit when limit <= 0, capped at
// MaxConvertLimit); longer input fails with ErrTooLarge. Output never starts
// with a byte order mark and line endings are copied unchanged. On error w
// may already hold a prefix, so cache writers must use a temporary file.
func ToUTF8(ctx context.Context, r io.Reader, w io.Writer, cs Charset, limit int64) (ConvertResult, error) {
	var res ConvertResult
	enc, ok := cs.Encoding()
	if !ok {
		return res, ErrUnsupportedCharset
	}
	limit = effectiveLimit(limit, DefaultConvertLimit, MaxConvertLimit)

	out := &utf8Sink{w: w, countReplacement: cs != UTF8}
	var sanitizer *utf8Sanitizer
	var t transform.Transformer
	if cs == UTF8 {
		sanitizer = &utf8Sanitizer{}
		t = sanitizer
	} else {
		t = enc.NewDecoder()
	}
	tw := transform.NewWriter(out, t)
	finish := func(err error) (ConvertResult, error) {
		res.Written, res.BOMStripped, res.Replaced = out.written, out.bomStripped, out.replaced
		if sanitizer != nil {
			res.Replaced += sanitizer.replaced
		}
		return res, err
	}

	in := &io.LimitedReader{R: r, N: limit + 1}
	buf := make([]byte, readChunk)
	empty := 0
	for {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		n, err := in.Read(buf)
		res.Read += int64(n)
		if res.Read > limit {
			res.Read = limit
			return finish(ErrTooLarge)
		}
		if n > 0 {
			empty = 0
			if _, werr := tw.Write(buf[:n]); werr != nil {
				return finish(werr)
			}
		} else if err == nil {
			if empty++; empty >= 100 {
				return finish(io.ErrNoProgress)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return finish(err)
		}
	}
	if err := tw.Close(); err != nil {
		return finish(err)
	}
	return finish(out.flush())
}

// utf8Sanitizer copies valid UTF-8 and replaces each invalid byte with
// U+FFFD. An incomplete sequence at end of input is replaced byte by byte.
type utf8Sanitizer struct {
	replaced int64
}

func (s *utf8Sanitizer) Reset() {}

func (s *utf8Sanitizer) Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error) {
	for nSrc < len(src) {
		c := src[nSrc]
		if c < utf8.RuneSelf {
			if nDst >= len(dst) {
				return nDst, nSrc, transform.ErrShortDst
			}
			dst[nDst] = c
			nDst++
			nSrc++
			continue
		}
		r, size := utf8.DecodeRune(src[nSrc:])
		if r == utf8.RuneError && size == 1 {
			if !atEOF && !utf8.FullRune(src[nSrc:]) {
				return nDst, nSrc, transform.ErrShortSrc
			}
			if len(dst)-nDst < 3 {
				return nDst, nSrc, transform.ErrShortDst
			}
			nDst += utf8.EncodeRune(dst[nDst:], utf8.RuneError)
			nSrc++
			s.replaced++
			continue
		}
		if len(dst)-nDst < size {
			return nDst, nSrc, transform.ErrShortDst
		}
		nDst += copy(dst[nDst:], src[nSrc:nSrc+size])
		nSrc += size
	}
	return nDst, nSrc, nil
}

// utf8Sink receives decoded UTF-8, drops one leading U+FEFF and optionally
// counts U+FFFD. It holds back an incomplete rune so a match is never split
// across writes.
type utf8Sink struct {
	w                io.Writer
	countReplacement bool
	started          bool
	bomStripped      bool
	pending          []byte
	written          int64
	replaced         int64
}

func (s *utf8Sink) Write(p []byte) (int, error) {
	data := p
	if len(s.pending) > 0 {
		data = append(s.pending, p...)
		s.pending = nil
	}
	// Keep an incomplete trailing rune for the next write.
	cut := len(data)
	for i := 1; i <= utf8.UTFMax-1 && i <= len(data); i++ {
		c := data[len(data)-i]
		if c < utf8.RuneSelf {
			break
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(data[len(data)-i:]) {
				cut = len(data) - i
			}
			break
		}
	}
	if cut < len(data) {
		s.pending = append([]byte(nil), data[cut:]...)
		data = data[:cut]
	}
	if !s.started && len(data) > 0 {
		s.started = true
		if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
			data = data[3:]
			s.bomStripped = true
		}
	}
	if s.countReplacement {
		for i := 0; i+2 < len(data); i++ {
			if data[i] == 0xef && data[i+1] == 0xbf && data[i+2] == 0xbd {
				s.replaced++
				i += 2
			}
		}
	}
	n, err := s.w.Write(data)
	s.written += int64(n)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// flush writes a held back fragment. Decoders only emit whole runes, so this
// is empty unless a decoder broke that rule.
func (s *utf8Sink) flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	data := s.pending
	s.pending = nil
	_, err := s.Write(data)
	if len(s.pending) > 0 {
		n, werr := s.w.Write(s.pending)
		s.written += int64(n)
		s.pending = nil
		if err == nil {
			err = werr
		}
	}
	return err
}
