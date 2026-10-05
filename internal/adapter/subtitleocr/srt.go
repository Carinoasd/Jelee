package subtitleocr

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Text bounds of one recognized cue: Tesseract output beyond them is noise
// for a subtitle picture, not text worth keeping.
const (
	maxCueLines     = 4
	maxCueLineRunes = 200
	// MaxTrackBytes bounds one generated SRT file; cues beyond it are dropped
	// and the track is marked truncated.
	MaxTrackBytes = 8 << 20
)

// Cue is one recognized subtitle.
type Cue struct {
	Start, End time.Duration
	Text       string
}

// cleanText normalizes Tesseract output: valid UTF-8 only, control and
// format characters removed, inner whitespace collapsed, empty lines
// dropped, at most maxCueLines lines of maxCueLineRunes runes. The result
// is plain text: nothing is interpreted as markup.
func cleanText(raw string) string {
	raw = strings.ToValidUTF8(raw, "")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.Map(func(r rune) rune {
			switch {
			case r == '\t' || r == '\r' || unicode.IsSpace(r):
				return ' '
			case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
				return -1
			}
			return r
		}, line)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxCueLineRunes {
			line = string([]rune(line)[:maxCueLineRunes])
		}
		lines = append(lines, line)
		if len(lines) == maxCueLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// srtTime formats a non-negative duration as HH:MM:SS,mmm.
func srtTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	hours := ms / 3600000
	minutes := ms / 60000 % 60
	seconds := ms / 1000 % 60
	millis := ms % 1000
	pad := func(value int64, width int) string {
		text := strconv.FormatInt(value, 10)
		for len(text) < width {
			text = "0" + text
		}
		return text
	}
	return pad(hours, 2) + ":" + pad(minutes, 2) + ":" + pad(seconds, 2) + "," + pad(millis, 3)
}

// FormatSRT writes cues as SubRip (UTF-8, no BOM, LF line ends). Cues with
// empty text are left out; the numbering stays consecutive. It reports
// whether cues were dropped to stay within MaxTrackBytes.
func FormatSRT(cues []Cue) ([]byte, bool) {
	var out bytes.Buffer
	number := 0
	for _, cue := range cues {
		if cue.Text == "" || cue.End <= cue.Start {
			continue
		}
		block := strconv.Itoa(number+1) + "\n" + srtTime(cue.Start) + " --> " + srtTime(cue.End) + "\n" + cue.Text + "\n\n"
		if out.Len()+len(block) > MaxTrackBytes {
			return out.Bytes(), true
		}
		number++
		out.WriteString(block)
	}
	return out.Bytes(), false
}
