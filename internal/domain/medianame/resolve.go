package medianame

import "strings"

// resolve activates the year and part candidates that should cut the title.
// Directory titles are final here; file titles wait for bare-number selection.
func (s *segment) resolve() {
	s.pickYear()
	if s.isFile {
		s.pickPart()
	}
	s.pureNum = s.onlyBare()
	if !s.isFile {
		s.computeTitle()
	}
}

func isFiller(c rune) bool {
	return isSep(c) || c == '[' || c == ']' || c == '(' || c == ')' || c == '【' || c == '】'
}

func (s *segment) textBetween(from, to int) bool {
	if from == s.start {
		return s.firstText < to // cached: avoids rescanning long filler prefixes
	}
	for i := from; i < to && i < s.n; i++ {
		if !isFiller(s.l[i]) {
			return true
		}
	}
	return false
}

// pickYear prefers the last parenthesized year; otherwise the last bare year
// that has title text before it, so "1917" alone stays a title and
// "Blade Runner 2049 2017" keeps 2049 in the title.
func (s *segment) pickYear() {
	pick := -1
	for i := range s.marks {
		if m := &s.marks[i]; m.kind == mkYear && m.paren {
			pick = i
		}
	}
	if pick < 0 {
		for i := range s.marks {
			if m := &s.marks[i]; m.kind == mkYear && s.textBetween(s.start, m.start) {
				pick = i
			}
		}
	}
	if pick >= 0 {
		s.marks[pick].active = true
	}
}

// pickPart keeps a part marker only when nothing but release tags follows it:
// "Movie (2010) Part 1" is a split file, "Deathly Hallows Part 2 (2011)" is a
// title.
func (s *segment) pickPart() {
	for i := range s.marks {
		m := &s.marks[i]
		if m.kind != mkPart {
			continue
		}
		j := m.end
		for j < s.n && isSep(s.l[j]) {
			j++
		}
		ok := j >= s.n
		for k := i + 1; !ok && k < len(s.marks); k++ {
			if n := s.marks[k]; n.start >= j {
				ok = n.start == j && n.active && (n.kind == mkNoise || n.kind == mkBracket)
				break
			}
		}
		if ok && s.textBetween(s.start, m.start) {
			m.active = true
			return
		}
	}
}

func (s *segment) onlyBare() bool {
	if !s.isFile || len(s.marks) != 1 || s.marks[0].kind != mkBare {
		return false
	}
	m := s.marks[0]
	return !s.textBetween(s.start, m.start) && !s.textBetween(m.end, s.n)
}

func (s *segment) first(kind markKind) *mark {
	for i := range s.marks {
		if s.marks[i].kind == kind {
			return &s.marks[i]
		}
	}
	return nil
}

func (s *segment) year() *mark {
	for i := range s.marks {
		if m := &s.marks[i]; m.kind == mkYear && m.active {
			return m
		}
	}
	return nil
}

// seasonMark returns the season evidence a directory contributes.
func (s *segment) seasonMark() *mark {
	for i := range s.marks {
		m := &s.marks[i]
		switch {
		case m.kind == mkSE || m.kind == mkSeason:
			return m
		case m.kind == mkSpecial && m.special != SpecialTheatrical:
			return m // season 0
		}
	}
	return nil
}

func (s *segment) noiseLimit() int {
	for _, m := range s.marks {
		if m.active && (m.kind == mkNoise || m.kind == mkBracket) {
			return m.start
		}
	}
	return s.n
}

// pickBare chooses a heuristic episode number, in order: " - 01" style,
// "[01]" style, then a plain number when a season context, a release-group
// tag or a numeric-only name makes it plausible.
func (s *segment) pickBare(seasonContext bool) *mark {
	limit := s.noiseLimit()
	var dash, bracket, plain, lead *mark
	for i := range s.marks {
		m := &s.marks[i]
		if m.kind != mkBare {
			continue
		}
		switch {
		case m.style == bareDash && m.start < limit && dash == nil:
			dash = m
		case m.style == bareBracket && bracket == nil:
			bracket = m
		case m.style == barePlain && m.start < limit:
			if !s.textBetween(s.start, m.start) {
				lead = m
			} else {
				plain = m
			}
		}
	}
	pick := dash
	if pick == nil {
		pick = bracket
	}
	if pick == nil && (seasonContext || s.pureNum) {
		pick = lead
		if pick == nil {
			pick = plain
		}
	}
	if pick == nil && s.group && plain != nil && !s.textBetween(plain.end, limit) {
		pick = plain
	}
	if pick != nil {
		pick.active = true
	}
	return pick
}

// maxTitleSteps bounds how many leading markers computeTitle may skip.
const maxTitleSteps = 8

// maxBracketTitle bounds the search for the end of a bracketed title.
const maxBracketTitle = 256

// computeTitle takes the first non-empty text run before a cutting marker,
// skipping leading season/special markers ("劇場版 Title") and reading a
// bracketed title that directly follows a group tag ("[Group][Title][01]").
func (s *segment) computeTitle() {
	start := s.start
	for step := 0; step < maxTitleSteps; step++ {
		var m *mark
		for i := range s.marks {
			if c := &s.marks[i]; c.active && c.start >= start {
				m = c
				break
			}
		}
		cut := s.n
		if m != nil {
			cut = m.start
		}
		if g := s.clean(start, cut); g != "" {
			s.title = g
			return
		}
		if m == nil {
			return
		}
		switch m.kind {
		case mkBracket:
			want := closer(s.l[m.start])
			end := -1
			for k := m.start + 1; k < s.n && k <= m.start+maxBracketTitle; k++ {
				if s.l[k] == want {
					end = k
					break
				}
			}
			if end < 0 {
				start = m.end
				continue
			}
			clear := true
			for _, o := range s.marks {
				if o.start >= end {
					break
				}
				if o.active && o.start > m.start {
					clear = false
					break
				}
			}
			if clear {
				if g := s.clean(m.start+1, end); g != "" {
					s.title = g
					return
				}
			}
			start = end + 1
		case mkSpecial, mkSeason:
			start = m.end
		default:
			return
		}
	}
}

const trimBoth = " -_.~,;:|/+"

func (s *segment) clean(from, to int) string {
	if from >= to {
		return ""
	}
	seg := s.r[from:to]
	spaced := false
	for _, c := range seg {
		if c == ' ' {
			spaced = true
			break
		}
	}
	var b strings.Builder
	prevSpace := false
	for _, c := range seg {
		if c == '_' || !spaced && c == '.' {
			c = ' '
		}
		if c == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		b.WriteRune(c)
	}
	out := b.String()
	for {
		t := strings.Trim(out, trimBoth)
		t = strings.TrimRight(t, "([{【")
		t = strings.TrimLeft(t, ")]}】")
		if t == out {
			break
		}
		out = t
	}
	return out
}
