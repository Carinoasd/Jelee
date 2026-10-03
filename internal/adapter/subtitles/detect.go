package subtitles

import (
	"bytes"
	"context"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

const (
	// DefaultDetectLimit is the scan budget used when DetectCharset gets a
	// non-positive limit. ASS files often open with long ASCII style blocks,
	// so the budget is larger than the evidence buffer.
	DefaultDetectLimit int64 = 1 << 20
	// MaxDetectLimit caps any requested scan budget.
	MaxDetectLimit int64 = 64 << 20
	// LowConfidence is the score below which a detection is marked Low and
	// callers should prefer an explicit override or keep the original bytes.
	LowConfidence = 0.5

	headBytes         = 4 << 10
	evidenceBytes     = 64 << 10
	evidenceLineBytes = 2 << 10
	readChunk         = 32 << 10
)

// Method says which rule decided a detection.
type Method string

const (
	MethodBOM          Method = "bom"
	MethodUTF16Pattern Method = "utf16-pattern"
	MethodASCII        Method = "ascii"
	MethodUTF8         Method = "utf8"
	MethodUTF8Mixed    Method = "utf8-mixed"
	MethodStatistical  Method = "statistical"
)

// Confidence describes how a charset was chosen. Score is in [0, 1]; Low is
// set whenever Score is below LowConfidence or the evidence is inconclusive
// (for example mixed encodings or an all-ASCII prefix of a longer file).
type Confidence struct {
	Score  float64
	Low    bool
	Method Method
	// BOM reports a byte order mark at the start of the input.
	BOM bool
	// RunnerUp is the second best statistical candidate, the fallback to try
	// when the chosen charset renders as mojibake.
	RunnerUp      Charset
	RunnerUpScore float64
	// Scanned counts bytes read from the input.
	Scanned int64
	// Partial is set when scanning stopped before end of input, because the
	// limit was reached (including input of exactly limit bytes) or enough
	// evidence had been collected.
	Partial bool
	// InvalidUTF8 counts broken sequences when Method is MethodUTF8Mixed.
	InvalidUTF8 int64
}

// DetectCharset guesses the charset of a text subtitle from at most limit
// bytes of r (DefaultDetectLimit when limit <= 0, capped at MaxDetectLimit).
// It never buffers the whole input. ctx is checked between reads; a reader
// that blocks must be unblocked by its owner.
func DetectCharset(ctx context.Context, r io.Reader, limit int64) (Charset, Confidence, error) {
	limit = effectiveLimit(limit, DefaultDetectLimit, MaxDetectLimit)
	in := &io.LimitedReader{R: r, N: limit}
	buf := make([]byte, readChunk)
	var conf Confidence
	eof, empty := false, 0
	read := func(p []byte) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := in.Read(p)
		conf.Scanned += int64(n)
		if err == io.EOF {
			eof = true
			err = nil
		}
		if n == 0 && err == nil && !eof {
			if empty++; empty >= 100 {
				return nil, io.ErrNoProgress
			}
		} else {
			empty = 0
		}
		return p[:n], err
	}

	head := make([]byte, 0, headBytes)
	for len(head) < headBytes && !eof {
		chunk, err := read(buf[:headBytes-len(head)])
		if err != nil {
			return CharsetUnknown, conf, err
		}
		head = append(head, chunk...)
	}
	if cs, ok, err := detectByHead(head, &conf); ok || err != nil {
		return cs, conf, err
	}

	s := scanner{}
	s.feed(head)
	for !eof && !s.evidenceFull() && !s.nul {
		chunk, err := read(buf)
		if err != nil {
			return CharsetUnknown, conf, err
		}
		s.feed(chunk)
	}
	s.finish()
	// A LimitedReader reports EOF once its budget is spent, so only a
	// remaining budget proves the real end of input.
	conf.Partial = in.N <= 0 || !eof
	if s.nul {
		return CharsetUnknown, conf, ErrNotText
	}
	if err := ctx.Err(); err != nil {
		return CharsetUnknown, conf, err
	}
	return decide(&s, &conf), conf, nil
}

func effectiveLimit(limit, def, maximum int64) int64 {
	if limit <= 0 {
		return def
	}
	return min(limit, maximum)
}

// detectByHead handles byte order marks and BOM-less UTF-16.
func detectByHead(head []byte, conf *Confidence) (Charset, bool, error) {
	switch {
	case bytes.HasPrefix(head, []byte{0xff, 0xfe, 0, 0}), bytes.HasPrefix(head, []byte{0, 0, 0xfe, 0xff}):
		return CharsetUnknown, false, ErrUnsupportedCharset
	case bytes.HasPrefix(head, []byte{0xef, 0xbb, 0xbf}):
		return bomResult(UTF8, conf)
	case bytes.HasPrefix(head, []byte{0xff, 0xfe}):
		return bomResult(UTF16LE, conf)
	case bytes.HasPrefix(head, []byte{0xfe, 0xff}):
		return bomResult(UTF16BE, conf)
	case bytes.HasPrefix(head, []byte{0x84, 0x31, 0x95, 0x33}):
		return bomResult(GB18030, conf)
	}
	if len(head) < 4 {
		return CharsetUnknown, false, nil
	}
	pairs := len(head) / 2
	zeroEven, zeroOdd := 0, 0
	for i := 0; i+1 < len(head); i += 2 {
		if head[i] == 0 {
			zeroEven++
		}
		if head[i+1] == 0 {
			zeroOdd++
		}
	}
	var cs Charset
	switch {
	case zeroOdd*4 >= pairs && zeroEven*10 <= zeroOdd:
		cs = UTF16LE
	case zeroEven*4 >= pairs && zeroOdd*10 <= zeroEven:
		cs = UTF16BE
	default:
		return CharsetUnknown, false, nil
	}
	conf.Method = MethodUTF16Pattern
	conf.Score = 0.95 * utf16Plausibility(head[:pairs*2], cs == UTF16LE)
	conf.Low = conf.Score < LowConfidence
	conf.Partial = true
	return cs, true, nil
}

func bomResult(cs Charset, conf *Confidence) (Charset, bool, error) {
	conf.Method, conf.BOM, conf.Score, conf.Partial = MethodBOM, true, 1, true
	return cs, true, nil
}

// utf16Plausibility is the share of code units that form valid, printable
// text. A truncated trailing surrogate pair is tolerated.
func utf16Plausibility(data []byte, little bool) float64 {
	unit := func(i int) uint16 {
		if little {
			return uint16(data[i]) | uint16(data[i+1])<<8
		}
		return uint16(data[i])<<8 | uint16(data[i+1])
	}
	total, bad := 0, 0
	for i := 0; i+1 < len(data); i += 2 {
		u := unit(i)
		total++
		switch {
		case u >= 0xd800 && u <= 0xdbff:
			if i+3 >= len(data) {
				continue
			}
			if low := unit(i + 2); low < 0xdc00 || low > 0xdfff {
				bad++
			} else {
				i += 2
			}
		case u >= 0xdc00 && u <= 0xdfff, u == 0xfffe, u == 0xffff:
			bad++
		case u < 0x20 && u != '\t' && u != '\n' && u != '\r':
			bad++
		}
	}
	if total == 0 {
		return 0
	}
	return max(0, 1-4*float64(bad)/float64(total))
}

// scanner validates UTF-8 over every scanned byte and keeps a bounded copy of
// the lines that contain non-ASCII bytes for statistical scoring.
type scanner struct {
	nul         bool
	carry       []byte
	utf8Multi   int64
	utf8Invalid int64
	line        []byte
	evidence    []byte
	full        bool
}

func (s *scanner) evidenceFull() bool { return s.full }

func (s *scanner) feed(p []byte) {
	if bytes.IndexByte(p, 0) >= 0 {
		s.nul = true
		return
	}
	s.validate(p, false)
	for len(p) > 0 && !s.full {
		end := len(p)
		if j := bytes.IndexByte(p, '\n'); j >= 0 {
			end = j + 1
		}
		end = min(end, evidenceLineBytes-len(s.line))
		s.line = append(s.line, p[:end]...)
		p = p[end:]
		if s.line[len(s.line)-1] == '\n' || len(s.line) == evidenceLineBytes {
			s.flushLine()
		}
	}
}

func (s *scanner) finish() {
	s.validate(nil, true)
	if !s.full {
		s.flushLine()
	}
}

func (s *scanner) flushLine() {
	if hasNonASCII(s.line) {
		if len(s.evidence)+len(s.line) > evidenceBytes {
			s.full = true
		} else {
			s.evidence = append(s.evidence, s.line...)
		}
	}
	s.line = s.line[:0]
}

// validate counts UTF-8 sequences. An incomplete sequence at the very end of
// the scanned bytes is a cut, not an error: it is how a limit or a truncated
// file ends.
func (s *scanner) validate(p []byte, final bool) {
	data := p
	if len(s.carry) > 0 {
		data = append(s.carry, p...)
		s.carry = nil
	}
	for i := 0; i < len(data); {
		if data[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data[i:]) {
				if !final {
					s.carry = append([]byte(nil), data[i:]...)
				}
				return
			}
			s.utf8Invalid++
			i++
			continue
		}
		s.utf8Multi++
		i += size
	}
}

func hasNonASCII(p []byte) bool {
	for _, c := range p {
		if c >= utf8.RuneSelf {
			return true
		}
	}
	return false
}

func decide(s *scanner, conf *Confidence) Charset {
	if s.utf8Invalid == 0 {
		if s.utf8Multi == 0 {
			// ASCII reads the same in every candidate; only a full scan
			// proves there is nothing else further on.
			conf.Method, conf.Score = MethodASCII, 1
			if conf.Partial {
				conf.Score, conf.Low = 0.6, true
			}
			return UTF8
		}
		conf.Method, conf.Score = MethodUTF8, 0.99
		if s.utf8Multi < 4 {
			conf.Score = 0.8
		}
		return UTF8
	}
	ranked := rankLegacy(s.evidence)
	if s.utf8Multi >= 16 && s.utf8Multi >= 8*s.utf8Invalid {
		conf.Method = MethodUTF8Mixed
		conf.InvalidUTF8 = s.utf8Invalid
		conf.Score = 0.49 * float64(s.utf8Multi) / float64(s.utf8Multi+s.utf8Invalid)
		conf.Low = true
		if len(ranked) > 0 {
			conf.RunnerUp, conf.RunnerUpScore = ranked[0].cs, ranked[0].score
		}
		return UTF8
	}
	conf.Method = MethodStatistical
	if len(ranked) == 0 || ranked[0].n == 0 {
		conf.Low = true
		return CharsetUnknown
	}
	best, second := ranked[0], ranked[1]
	conf.RunnerUp, conf.RunnerUpScore = second.cs, second.score
	margin := min(1, 0.5+(best.score-second.score)/0.4)
	amount := min(1, 0.4+float64(best.n)/12)
	conf.Score = max(0, min(1, best.score)) * margin * amount
	conf.Low = conf.Score < LowConfidence
	return best.cs
}

type rankedCharset struct {
	cs    Charset
	score float64
	n     int
}

var legacyCandidates = []struct {
	cs      Charset
	profile *langProfile
}{
	{GB18030, &profileHans},
	{Big5, &profileHant},
	{ShiftJIS, &profileJapanese},
	{EUCJP, &profileJapanese},
	{EUCKR, &profileKorean},
	{Windows1252, nil},
}

// rankLegacy scores each legacy candidate on the evidence, best first. Ties
// keep candidate order.
func rankLegacy(evidence []byte) []rankedCharset {
	out := make([]rankedCharset, 0, len(legacyCandidates))
	for _, c := range legacyCandidates {
		var r rankedCharset
		if c.profile == nil {
			r = scoreWindows1252(evidence)
		} else {
			r = scoreMultibyte(c.cs, c.profile, evidence)
		}
		r.cs = c.cs
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].score > out[j-1].score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func scoreMultibyte(cs Charset, profile *langProfile, evidence []byte) rankedCharset {
	enc, _ := cs.Encoding()
	decoded, err := enc.NewDecoder().Bytes(evidence)
	if err != nil {
		return rankedCharset{score: -1}
	}
	var sum float64
	n, bad := 0, 0
	for _, r := range string(decoded) {
		if r < utf8.RuneSelf {
			continue
		}
		n++
		if invalidRune(r) {
			bad++
			continue
		}
		sum += profile.weight(r)
	}
	if n == 0 {
		return rankedCharset{}
	}
	return rankedCharset{score: (sum - 2*float64(bad)) / float64(n), n: n}
}

// scoreWindows1252 rewards accented letters and typographic punctuation in
// short runs next to ASCII letters; long high-byte runs are what double-byte
// text looks like through a single-byte table.
func scoreWindows1252(evidence []byte) rankedCharset {
	var sum float64
	n, bad := 0, 0
	for i := 0; i < len(evidence); {
		if evidence[i] < utf8.RuneSelf {
			i++
			continue
		}
		j := i
		for j < len(evidence) && evidence[j] >= utf8.RuneSelf {
			j++
		}
		run := j - i
		nearLetter := (i > 0 && isASCIILetter(evidence[i-1])) || (j < len(evidence) && isASCIILetter(evidence[j]))
		for k := i; k < j; k++ {
			n++
			r := charmap.Windows1252.DecodeByte(evidence[k])
			switch {
			case invalidRune(r):
				bad++
			case latinLetter(r):
				switch {
				case run == 1 && nearLetter:
					sum += 1
				case run == 1:
					sum += 0.6
				case run == 2:
					sum += 0.5
				default:
					sum += 0.05
				}
			case latinPunct(r):
				if run <= 2 {
					sum += 0.8
				} else {
					sum += 0.05
				}
			case run <= 2:
				sum += 0.1
			}
		}
		i = j
	}
	if n == 0 {
		return rankedCharset{}
	}
	return rankedCharset{score: (sum - 2*float64(bad)) / float64(n), n: n}
}

func isASCIILetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

func latinLetter(r rune) bool {
	switch r {
	case 'Š', 'š', 'Œ', 'œ', 'Ž', 'ž', 'Ÿ', 'ƒ':
		return true
	}
	return r >= 0xc0 && r <= 0xff && r != 0xd7 && r != 0xf7
}

func latinPunct(r rune) bool {
	switch r {
	case '‘', '’', '“', '”', '–', '—', '…', '•', '€', '«', '»', '°', '·', '¿', '¡', ' ', '©', '®', '™', '‹', '›', 'º', 'ª':
		return true
	}
	return false
}

func invalidRune(r rune) bool {
	switch {
	case r == utf8.RuneError, r == 0xfffe, r == 0xffff:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0xe000 && r <= 0xf8ff, r >= 0xf0000:
		return true
	}
	return false
}
