// Package medianame infers movie and episode identity from library-relative
// media paths. It is a pure, allocation-bounded parser: it never touches the
// file system and treats every input as untrusted.
//
// Precedence follows G21.2. Callers must let NFO data and external IDs win over
// anything returned here; inside this package explicit file naming wins over
// directory structure, which in turn wins over numeric heuristics.
package medianame

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxPathBytes is the hard upper bound for an accepted relative path.
const MaxPathBytes = 4096

// MaxSegments bounds directory depth so directory evidence stays cheap.
const MaxSegments = 64

const maxEvidenceRunes = 64

type Kind uint8

const (
	KindUnknown Kind = iota
	KindMovie
	KindEpisode
)

func (k Kind) String() string {
	switch k {
	case KindMovie:
		return "movie"
	case KindEpisode:
		return "episode"
	}
	return "unknown"
}

// Confidence gates automatic writes (G14.4): only High may be applied without
// review; Medium and Low must be confirmed by a person or stronger metadata.
type Confidence uint8

const (
	ConfidenceNone Confidence = iota
	ConfidenceLow
	ConfidenceMedium
	ConfidenceHigh
)

func (c Confidence) String() string {
	switch c {
	case ConfidenceLow:
		return "low"
	case ConfidenceMedium:
		return "medium"
	case ConfidenceHigh:
		return "high"
	}
	return "none"
}

// Special classifies non-regular episodes and theatrical releases (G21.3).
type Special uint8

const (
	SpecialNone       Special = iota
	SpecialSeason             // Specials directory, Season 0, S00
	SpecialSP                 // SP, SPs, スペシャル
	SpecialOVA                // OVA, OAV
	SpecialOAD                // OAD
	SpecialExtra              // 特別篇, 番外篇
	SpecialTheatrical         // 劇場版, 剧场版, Gekijouban
)

func (s Special) String() string {
	return [...]string{"none", "season", "sp", "ova", "oad", "extra", "theatrical"}[s]
}

type Source uint8

const (
	SourceNone Source = iota
	SourceDirectory
	SourceFilename
)

func (s Source) String() string {
	return [...]string{"none", "directory", "filename"}[s]
}

type Field uint8

const (
	FieldTitle Field = iota + 1
	FieldYear
	FieldSeason
	FieldEpisode
	FieldAbsolute
	FieldPart
	FieldDate
	FieldSpecial
)

func (f Field) String() string {
	return [...]string{"", "title", "year", "season", "episode", "absolute", "part", "date", "special"}[f]
}

// Evidence explains where one field came from. Overridden evidence was seen
// but lost to a higher-priority source (for example a directory season that
// disagrees with an explicit filename season).
type Evidence struct {
	Field      Field
	Source     Source
	Rule       string
	Text       string
	Overridden bool
}

// Rejection reports why an input was refused; refused inputs carry no fields.
type Rejection uint8

const (
	RejectNone Rejection = iota
	RejectEmpty
	RejectTooLong
	RejectInvalidUTF8
	RejectControl
	RejectNotRelative
	RejectBadSegment
	RejectTooDeep
)

func (r Rejection) String() string {
	return [...]string{"none", "empty", "too-long", "invalid-utf8", "control-character", "not-relative", "bad-segment", "too-deep"}[r]
}

// Options tunes recognition. The zero value is the conservative default.
type Options struct {
	// DateEpisodes enables daily-show naming such as 2024-01-31 (G21.3).
	// When disabled dates are ignored instead of being read as a year.
	DateEpisodes bool
	// MaxPathBytes lowers the accepted path length; zero or values above the
	// package limit use MaxPathBytes.
	MaxPathBytes int
}

type Date struct{ Year, Month, Day int }

func (d Date) IsZero() bool { return d.Year == 0 }

// Parsed is the parser result. Season and episode use explicit presence flags
// because season 0 and episode 0 are meaningful values.
type Parsed struct {
	Rejected   Rejection
	Kind       Kind
	Title      string
	Year       int
	HasSeason  bool
	Season     int
	HasEpisode bool
	Episode    int
	EpisodeEnd int // equals Episode for single episodes
	// Absolute mirrors Episode when the number came without any season context
	// (anime absolute numbering); zero otherwise.
	Absolute   int
	Part       int
	Date       Date
	Special    Special
	Conflict   bool
	Confidence Confidence
	Evidence   []Evidence
}

// ParsePath parses a library-relative, slash-separated media path.
func ParsePath(relativePath string, opts Options) Parsed {
	if r := validate(relativePath, opts); r != RejectNone {
		return Parsed{Rejected: r}
	}
	parts := strings.Split(relativePath, "/")
	file := analyze(parts[len(parts)-1], true, opts)
	dirs := make([]*segment, 0, len(parts)-1)
	for i := len(parts) - 2; i >= 0; i-- { // nearest directory first
		dirs = append(dirs, analyze(parts[i], false, opts))
	}
	return combine(file, dirs)
}

func validate(p string, opts Options) Rejection {
	limit := MaxPathBytes
	if opts.MaxPathBytes > 0 && opts.MaxPathBytes < limit {
		limit = opts.MaxPathBytes
	}
	switch {
	case p == "":
		return RejectEmpty
	case len(p) > limit:
		return RejectTooLong
	case !utf8.ValidString(p):
		return RejectInvalidUTF8
	}
	for _, r := range p {
		if unicode.IsControl(r) || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 || r == 0xFEFF {
			return RejectControl
		}
	}
	if p[0] == '/' {
		return RejectNotRelative
	}
	if strings.Count(p, "/") >= MaxSegments {
		return RejectTooDeep
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." || strings.TrimSpace(s) == "" {
			return RejectBadSegment
		}
	}
	return RejectNone
}

func snippet(r []rune, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(r) {
		end = len(r)
	}
	if end-start > maxEvidenceRunes {
		end = start + maxEvidenceRunes
	}
	return string(r[start:end])
}

func combine(file *segment, dirs []*segment) Parsed {
	out := Parsed{}
	add := func(e Evidence) { out.Evidence = append(out.Evidence, e) }
	explicitEpisode, explicitSeason, heuristic := false, false, false

	// Directory season context: the nearest directory carrying a season marker.
	var dirSeason *mark
	var dirSeasonSeg *segment
	var dirSpecial *mark
	for _, d := range dirs {
		if m := d.seasonMark(); m != nil && dirSeason == nil {
			dirSeason, dirSeasonSeg = m, d
		}
		if m := d.first(mkSpecial); m != nil && dirSpecial == nil {
			dirSpecial = m
		}
		if dirSeason != nil {
			break
		}
	}

	// Season and episode from explicit filename naming.
	if m := file.first(mkSE); m != nil {
		out.HasSeason, out.Season = true, m.season
		out.HasEpisode, out.Episode, out.EpisodeEnd = true, m.ep, m.epEnd
		explicitSeason, explicitEpisode = true, true
		add(Evidence{FieldSeason, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
		add(Evidence{FieldEpisode, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
	} else {
		if m := file.first(mkSeason); m != nil {
			out.HasSeason, out.Season, explicitSeason = true, m.season, true
			add(Evidence{FieldSeason, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
		}
		if m := file.first(mkEpisode); m != nil {
			out.HasEpisode, out.Episode, out.EpisodeEnd, explicitEpisode = true, m.ep, m.epEnd, true
			add(Evidence{FieldEpisode, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
		}
	}

	// Specials: filename first, then directory.
	if m := file.first(mkSpecial); m != nil {
		out.Special = m.special
		add(Evidence{FieldSpecial, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
		if m.special != SpecialTheatrical {
			if !out.HasSeason {
				out.HasSeason, out.Season, explicitSeason = true, 0, true
			}
			if m.hasNum && !out.HasEpisode {
				out.HasEpisode, out.Episode, out.EpisodeEnd, explicitEpisode = true, m.ep, m.ep, true
				add(Evidence{FieldEpisode, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
			}
		}
	} else if dirSpecial != nil && (dirSpecial.special == SpecialTheatrical || !out.HasSeason || out.Season == 0) {
		out.Special = dirSpecial.special
		add(Evidence{FieldSpecial, SourceDirectory, dirSpecial.rule, "", false})
	}

	// Directory season applies only when the filename did not name one.
	if dirSeason != nil {
		text := snippet(dirSeasonSeg.r, dirSeason.start, dirSeason.end)
		if out.HasSeason {
			if out.Season != dirSeason.season {
				out.Conflict = true
				add(Evidence{FieldSeason, SourceDirectory, dirSeason.rule, text, true})
			}
		} else {
			out.HasSeason, out.Season = true, dirSeason.season
			add(Evidence{FieldSeason, SourceDirectory, dirSeason.rule, text, false})
		}
	}
	if out.HasSeason && out.Season == 0 && out.Special == SpecialNone {
		out.Special = SpecialSeason
	}

	// Date-based episodes.
	if m := file.first(mkDate); m != nil && m.active && !out.HasEpisode {
		out.Date = Date{m.y, m.mo, m.d}
		explicitEpisode = true
		add(Evidence{FieldDate, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
	}

	// Heuristic bare numbers.
	seasonContext := dirSeason != nil || out.HasSeason
	if !out.HasEpisode && out.Date.IsZero() && out.Special != SpecialTheatrical {
		if m := file.pickBare(seasonContext); m != nil {
			out.HasEpisode, out.Episode, out.EpisodeEnd, heuristic = true, m.ep, m.ep, true
			add(Evidence{FieldEpisode, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
		}
	}
	if out.HasEpisode && !out.HasSeason && out.Special == SpecialNone {
		out.Absolute = out.Episode
		add(Evidence{FieldAbsolute, SourceFilename, "no-season-context", "", false})
	}

	// Part, year and title.
	if m := file.first(mkPart); m != nil && m.active {
		out.Part = m.ep
		add(Evidence{FieldPart, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
	}
	yearParen := false
	if m := file.year(); m != nil {
		out.Year, yearParen = m.y, m.paren
		add(Evidence{FieldYear, SourceFilename, m.rule, snippet(file.r, m.start, m.end), false})
	}
	file.computeTitle()
	out.Title = file.title
	if out.Title != "" {
		add(Evidence{FieldTitle, SourceFilename, "leading-text", snippet([]rune(out.Title), 0, maxEvidenceRunes), false})
	}
	for _, d := range dirs {
		if d.title == "" {
			continue
		}
		if out.Title == "" {
			out.Title = d.title
			add(Evidence{FieldTitle, SourceDirectory, "nearest-named-directory", snippet([]rune(d.title), 0, maxEvidenceRunes), false})
		}
		if out.Year == 0 {
			if m := d.year(); m != nil {
				out.Year, yearParen = m.y, true // a year in the owning folder name is deliberate
				add(Evidence{FieldYear, SourceDirectory, m.rule, snippet(d.r, m.start, m.end), false})
			}
		}
		break
	}

	// Kind inference.
	switch {
	case out.HasEpisode && out.Special != SpecialTheatrical || !out.Date.IsZero():
		out.Kind = KindEpisode
	case out.Special == SpecialTheatrical:
		out.Kind = KindMovie
	case out.Special != SpecialNone || dirSeason != nil || explicitSeason:
		out.Kind = KindEpisode
	case out.Year != 0:
		out.Kind = KindMovie
	}

	if out.Kind == KindMovie && out.HasSeason {
		out.HasSeason, out.Season = false, 0 // a theatrical release inside a season folder
	}

	// Confidence.
	c := ConfidenceNone
	switch out.Kind {
	case KindEpisode:
		switch {
		case heuristic:
			c = ConfidenceLow
			if out.HasSeason {
				c = ConfidenceMedium
			}
		case !out.HasEpisode && out.Date.IsZero():
			c = ConfidenceLow
		case explicitEpisode && out.HasSeason || !out.Date.IsZero():
			c = ConfidenceHigh
		default:
			c = ConfidenceMedium
		}
	case KindMovie:
		switch {
		case out.Special == SpecialTheatrical:
			c = ConfidenceMedium
		case yearParen:
			c = ConfidenceHigh
		default:
			c = ConfidenceMedium
		}
	default:
		if out.Title != "" {
			c = ConfidenceLow
		}
	}
	if out.Title == "" && c > ConfidenceLow {
		c = ConfidenceLow
	}
	if out.Conflict && c > ConfidenceMedium {
		c = ConfidenceMedium
	}
	out.Confidence = c
	return out
}
