package medianame

import "strings"

// The scanner is a single left-to-right pass. Every matcher inspects a
// bounded window (numbers are capped at a few digits, separators at two
// runes) and the scanner skips whole words that do not match, so total work
// is linear in the input length; there is no backtracking regex.

type markKind uint8

const (
	mkSE markKind = iota + 1 // season and episode in one token
	mkSeason
	mkEpisode
	mkSpecial
	mkYear
	mkDate
	mkPart
	mkNoise
	mkBare
	mkBracket
)

type bareStyle uint8

const (
	barePlain bareStyle = iota
	bareDash
	bareBracket
)

type mark struct {
	kind            markKind
	start, end      int
	season, ep      int
	epEnd           int
	hasNum          bool
	special         Special
	y, mo, d        int
	paren           bool
	style           bareStyle
	rule            string
	active          bool // cuts the title
	afterGroupStart bool // has title text before it
}

type segment struct {
	r       []rune // normalized, case preserved
	l       []rune // normalized, ASCII lower-cased
	n       int    // stem length (extension removed for files)
	start   int    // first rune after a leading release-group tag
	group   bool
	marks   []mark
	title   string
	isFile  bool
	pureNum bool
	// firstText is the first non-filler rune at or after start (n if none).
	firstText int
}

func normalizeRune(c rune) rune {
	switch {
	case c >= 0xFF01 && c <= 0xFF5E: // full-width ASCII
		return c - 0xFEE0
	case c == 0x3000:
		return ' '
	case c == 0x301C:
		return '~'
	}
	return c
}

func lower(c rune) rune {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func isDigit(c rune) bool  { return c >= '0' && c <= '9' }
func isLetter(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c rune) bool  { return isDigit(c) || isLetter(c) }
func isSep(c rune) bool    { return c == ' ' || c == '.' || c == '_' || c == '-' }

func analyze(name string, isFile bool, opts Options) *segment {
	src := []rune(name)
	s := &segment{r: make([]rune, len(src)), l: make([]rune, len(src)), isFile: isFile}
	for i, c := range src {
		c = normalizeRune(c)
		s.r[i], s.l[i] = c, lower(c)
	}
	s.n = len(s.l)
	if isFile {
		s.n = stemEnd(s.l)
	}
	s.start = s.groupEnd()
	s.firstText = s.n
	for i := s.start; i < s.n; i++ {
		if !isFiller(s.l[i]) {
			s.firstText = i
			break
		}
	}
	s.scan(opts)
	s.resolve()
	return s
}

// stemEnd strips a short alphanumeric extension containing a letter.
func stemEnd(l []rune) int {
	for i := len(l) - 1; i > 0 && len(l)-i <= 6; i-- {
		if l[i] == '.' {
			ext := l[i+1:]
			letter := false
			for _, c := range ext {
				if !isAlnum(c) {
					return len(l)
				}
				letter = letter || isLetter(c)
			}
			if len(ext) >= 2 && letter {
				return i
			}
			return len(l)
		}
	}
	return len(l)
}

func closer(c rune) rune {
	switch c {
	case '[':
		return ']'
	case '【':
		return '】'
	case '(':
		return ')'
	}
	return 0
}

// groupEnd skips a leading fansub/release group tag such as "[Group]".
func (s *segment) groupEnd() int {
	if s.n == 0 || (s.l[0] != '[' && s.l[0] != '【') {
		return 0
	}
	want := closer(s.l[0])
	for i := 1; i < s.n && i < 128; i++ {
		if s.l[i] == want {
			if i == 1 {
				return 0
			}
			j := i + 1
			for j < s.n && (s.l[j] == ' ' || s.l[j] == '_' || s.l[j] == '-' || s.l[j] == '.') {
				j++
			}
			if j >= s.n {
				return 0 // the tag is the whole name
			}
			s.group = true
			return j
		}
	}
	return 0
}

func (s *segment) at(i int) rune {
	if i < 0 || i >= s.n {
		return 0
	}
	return s.l[i]
}

func (s *segment) wordStart(i int) bool { return i == 0 || !isAlnum(s.l[i-1]) }
func (s *segment) wordEnd(i int) bool   { return i >= s.n || !isAlnum(s.l[i]) }

// alone reports whether [from,to) is the only text in the name, which is how
// ambiguous words such as "Special" are accepted ("Special Forces" is a title).
func (s *segment) alone(from, to int) bool {
	return s.wordEnd(to) && !s.textBetween(s.start, from) && !s.textBetween(to, s.n)
}

// readNum reads 1..max ASCII digits; it fails when more digits follow.
func (s *segment) readNum(i, max int) (int, int, bool) {
	v, j := 0, i
	for j < s.n && isDigit(s.l[j]) {
		if j-i == max {
			return 0, i, false
		}
		v = v*10 + int(s.l[j]-'0')
		j++
	}
	return v, j, j > i
}

var cjkDigit = map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '兩': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}

// readCJKNum reads ASCII digits or Chinese numerals up to 999.
func (s *segment) readCJKNum(i int) (int, int, bool) {
	if v, j, ok := s.readNum(i, 4); ok {
		return v, j, true
	}
	total, cur, j := 0, -1, i
	for j < s.n && j-i < 8 {
		c := s.l[j]
		if d, ok := cjkDigit[c]; ok {
			cur = d
		} else if c == '十' || c == '百' {
			unit := 10
			if c == '百' {
				unit = 100
			}
			if cur < 0 {
				cur = 1
			}
			total += cur * unit
			cur = -1
		} else {
			break
		}
		j++
	}
	if j == i {
		return 0, i, false
	}
	if cur > 0 {
		total += cur
	}
	return total, j, true
}

func (s *segment) hasPrefix(i int, p string) bool {
	j := i
	for _, c := range p {
		if j >= s.n || s.l[j] != c {
			return false
		}
		j++
	}
	return true
}

// skipSep consumes at most max separator runes.
func (s *segment) skipSep(i, max int, seps string) int {
	for k := 0; k < max && i < s.n && strings.ContainsRune(seps, s.l[i]); k++ {
		i++
	}
	return i
}

func (s *segment) add(m mark) { s.marks = append(s.marks, m) }

func (s *segment) scan(opts Options) {
	for i := s.start; i < s.n; {
		c := s.l[i]
		if c == '[' || c == '【' {
			s.add(mark{kind: mkBracket, start: i, end: i + 1, active: true})
			i++
			continue
		}
		if end, ok := s.matchCJK(i); ok {
			i = end
			continue
		}
		if c == '#' {
			if v, j, ok := s.readNum(i+1, 4); ok && s.wordEnd(j) {
				s.add(mark{kind: mkEpisode, start: i, end: j, ep: v, epEnd: v, rule: "episode-hash", active: true})
				i = j
				continue
			}
		}
		if isAlnum(c) {
			if s.wordStart(i) {
				if end, ok := s.matchWord(i, opts); ok {
					i = end
					continue
				}
			}
			for i < s.n && isAlnum(s.l[i]) {
				i++
			}
			continue
		}
		i++
	}
}

var cjkSpecials = []struct {
	text    string
	special Special
}{
	{"劇場版", SpecialTheatrical}, {"剧场版", SpecialTheatrical},
	{"特別篇", SpecialExtra}, {"特别篇", SpecialExtra}, {"特別編", SpecialExtra}, {"特别编", SpecialExtra},
	{"番外篇", SpecialExtra}, {"番外編", SpecialExtra}, {"番外", SpecialExtra},
	{"スペシャル", SpecialSP},
}

func (s *segment) matchCJK(i int) (int, bool) {
	c := s.l[i]
	switch {
	case c == '第':
		j := s.skipSep(i+1, 1, " ")
		a, j, ok := s.readCJKNum(j)
		if !ok {
			return 0, false
		}
		b := a
		j = s.skipSep(j, 1, " ")
		if k := j; k < s.n && strings.ContainsRune("-~至到", s.l[k]) {
			k = s.skipSep(k+1, 1, " ")
			if k < s.n && s.l[k] == '第' {
				k++
			}
			if v, k2, ok := s.readCJKNum(k); ok && v >= a {
				b, j = v, s.skipSep(k2, 1, " ")
			}
		}
		switch {
		case j < s.n && strings.ContainsRune("季期", s.l[j]):
			s.add(mark{kind: mkSeason, start: i, end: j + 1, season: a, rule: "season-cjk", active: true})
			return j + 1, true
		case s.hasPrefix(j, "シーズン"):
			s.add(mark{kind: mkSeason, start: i, end: j + 4, season: a, rule: "season-cjk", active: true})
			return j + 4, true
		case j < s.n && strings.ContainsRune("集話话回", s.l[j]):
			s.add(mark{kind: mkEpisode, start: i, end: j + 1, ep: a, epEnd: b, rule: "episode-cjk", active: true})
			return j + 1, true
		}
		return 0, false
	case s.hasPrefix(i, "シーズン"):
		j := s.skipSep(i+4, 1, " ._-")
		if v, j, ok := s.readCJKNum(j); ok {
			s.add(mark{kind: mkSeason, start: i, end: j, season: v, rule: "season-cjk", active: true})
			return j, true
		}
		return 0, false
	}
	for _, sp := range cjkSpecials {
		if s.hasPrefix(i, sp.text) {
			j := i + len([]rune(sp.text))
			m := mark{kind: mkSpecial, start: i, end: j, special: sp.special, rule: "special-cjk", active: true}
			if sp.special != SpecialTheatrical {
				if v, k, ok := s.readNum(s.skipSep(j, 1, " "), 3); ok && s.wordEnd(k) {
					m.ep, m.hasNum, m.end = v, true, k
				}
			}
			s.add(m)
			return m.end, true
		}
	}
	return 0, false
}

var noiseWords = map[string]bool{
	"4k": true, "8k": true, "uhd": true, "hdr": true, "hdr10": true, "dv": true, "dovi": true, "sdr": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true, "av1": true, "vp9": true, "xvid": true, "divx": true,
	"10bit": true, "8bit": true, "hi10p": true, "bluray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true,
	"webrip": true, "webdl": true, "hdtv": true, "dvdrip": true, "bd": true,
	"aac": true, "ac3": true, "eac3": true, "dts": true, "truehd": true, "atmos": true, "flac": true, "opus": true, "mp3": true, "ddp": true,
	"proper": true, "repack": true, "extended": true, "unrated": true, "remastered": true, "imax": true,
	"chs": true, "cht": true, "big5": true, "gb": true, "nf": true, "amzn": true, "dsnp": true, "hmax": true, "atvp": true,
}

var noisePhrases = []string{"web-dl", "web-rip", "blu-ray", "h.264", "h.265", "x.264", "x.265", "dts-hd", "hdr10+", "ddp5.1", "dd5.1", "aac2.0", "dolby vision"}

func (s *segment) matchWord(i int, opts Options) (int, bool) {
	l := s.l
	if isDigit(l[i]) {
		return s.matchDigitWord(i, opts)
	}
	for _, p := range noisePhrases {
		if s.hasPrefix(i, p) && s.wordEnd(i+len(p)) {
			s.add(mark{kind: mkNoise, start: i, end: i + len(p), active: true})
			return i + len(p), true
		}
	}
	// SxxEyy family.
	if l[i] == 's' {
		if end, ok := s.matchSE(i); ok {
			return end, true
		}
	}
	switch {
	case s.hasPrefix(i, "season"):
		j := s.skipSep(i+6, 2, " ._-")
		if v, k, ok := s.readNum(j, 4); ok && s.wordEnd(k) {
			s.add(mark{kind: mkSeason, start: i, end: k, season: v, rule: "season-word", active: true})
			return k, true
		}
	case s.hasPrefix(i, "specials") && s.alone(i, i+8):
		s.add(mark{kind: mkSpecial, start: i, end: i + 8, special: SpecialSeason, rule: "special-word", active: true})
		return i + 8, true
	case s.hasPrefix(i, "special") && s.alone(i, i+7):
		s.add(mark{kind: mkSpecial, start: i, end: i + 7, special: SpecialSeason, rule: "special-word", active: true})
		return i + 7, true
	case s.hasPrefix(i, "sps") && s.wordEnd(i+3):
		s.add(mark{kind: mkSpecial, start: i, end: i + 3, special: SpecialSP, rule: "special-sp", active: true})
		return i + 3, true
	case s.hasPrefix(i, "sp"):
		if end, ok := s.matchSpecialNum(i, 2, SpecialSP, "special-sp"); ok {
			return end, true
		}
	case s.hasPrefix(i, "ova") || s.hasPrefix(i, "oav"):
		if end, ok := s.matchSpecialNum(i, 3, SpecialOVA, "special-ova"); ok {
			return end, true
		}
	case s.hasPrefix(i, "oad"):
		if end, ok := s.matchSpecialNum(i, 3, SpecialOAD, "special-oad"); ok {
			return end, true
		}
	case s.hasPrefix(i, "gekijouban") && s.wordEnd(i+10):
		s.add(mark{kind: mkSpecial, start: i, end: i + 10, special: SpecialTheatrical, rule: "special-theatrical", active: true})
		return i + 10, true
	case s.hasPrefix(i, "episode"):
		if end, ok := s.matchEpisode(i, 7, "episode-word"); ok {
			return end, true
		}
	case s.hasPrefix(i, "ep"):
		if end, ok := s.matchEpisode(i, 2, "episode-ep"); ok {
			return end, true
		}
	case l[i] == 'e':
		if end, ok := s.matchEpisode(i, 1, "episode-e"); ok {
			return end, true
		}
	}
	for _, p := range [...]string{"part", "pt", "cd", "disc", "disk", "dvd"} {
		if s.hasPrefix(i, p) {
			j := s.skipSep(i+len(p), 1, " ._-")
			if v, k, ok := s.readNum(j, 2); ok && v > 0 && s.wordEnd(k) {
				s.add(mark{kind: mkPart, start: i, end: k, ep: v, rule: "part-" + p})
				return k, true
			}
			break
		}
	}
	// Noise word: the whole alphanumeric run must be a known release tag.
	j := i
	for j < s.n && isAlnum(l[j]) {
		j++
	}
	if j-i <= 12 && noiseWords[string(l[i:j])] {
		s.add(mark{kind: mkNoise, start: i, end: j, active: true})
		return j, true
	}
	return 0, false
}

// matchSE handles S01E02, s1e1, S01 E02, S01E01-E03, S01E01E02, S01E01-03 and
// S01E01-S01E02. A lone S01 becomes a season marker.
func (s *segment) matchSE(i int) (int, bool) {
	season, j, ok := s.readNum(i+1, 4)
	if !ok {
		return 0, false
	}
	k := s.skipSep(j, 1, " ._-")
	if s.at(k) == 'e' {
		k++
		if s.at(k) == 'p' {
			k++
		}
		if ep, k2, ok := s.readNum(k, 4); ok {
			end, last := s.extendRange(k2, ep, season)
			if s.wordEnd(end) {
				s.add(mark{kind: mkSE, start: i, end: end, season: season, ep: ep, epEnd: last, rule: "season-episode", active: true})
				return end, true
			}
		}
	}
	if s.wordEnd(j) {
		s.add(mark{kind: mkSeason, start: i, end: j, season: season, rule: "season-short", active: true})
		return j, true
	}
	return 0, false
}

// extendRange consumes multi-episode continuations and returns the new end and
// the last episode. Continuations must strictly increase.
func (s *segment) extendRange(i, first, season int) (int, int) {
	end, last := s.skipVersion(i), first
	for steps := 0; steps < 64; steps++ {
		k := end
		dash := false
		if c := s.at(k); c == '-' || c == '~' {
			k++
			dash = true
		}
		hasE := false
		if dash && s.at(k) == 's' {
			v, k2, ok := s.readNum(k+1, 4)
			if !ok || v != season || s.at(k2) != 'e' {
				break
			}
			k = k2
		}
		if s.at(k) == 'e' {
			k++
			if s.at(k) == 'p' {
				k++
			}
			hasE = true
		}
		if !dash && !hasE {
			break
		}
		max := 4
		if !hasE {
			max = 3
		}
		v, k2, ok := s.readNum(k, max)
		if !ok || v <= last {
			break
		}
		k2 = s.skipVersion(k2)
		if !s.wordEnd(k2) && s.at(k2) != 'e' {
			break
		}
		last, end = v, k2
	}
	return end, last
}

func (s *segment) skipVersion(i int) int {
	if s.at(i) == 'v' && isDigit(s.at(i+1)) && !isAlnum(s.at(i+2)) {
		return i + 2
	}
	return i
}

func (s *segment) matchSpecialNum(i, plen int, sp Special, rule string) (int, bool) {
	j := i + plen
	m := mark{kind: mkSpecial, start: i, special: sp, rule: rule, active: true}
	if s.wordEnd(j) {
		m.end = j
		k := s.skipSep(j, 1, " ._-")
		if v, k2, ok := s.readNum(k, 3); ok && k > j && s.wordEnd(s.skipVersion(k2)) && s.at(j) != '-' {
			m.ep, m.hasNum, m.end = v, true, s.skipVersion(k2)
		}
		s.add(m)
		return m.end, true
	}
	if v, k, ok := s.readNum(j, 3); ok && s.wordEnd(s.skipVersion(k)) {
		m.ep, m.hasNum, m.end = v, true, s.skipVersion(k)
		s.add(m)
		return m.end, true
	}
	return 0, false
}

func (s *segment) matchEpisode(i, plen int, rule string) (int, bool) {
	j := i + plen
	if plen > 1 {
		j = s.skipSep(j, 2, " ._-")
	}
	v, k, ok := s.readNum(j, 4)
	if !ok {
		return 0, false
	}
	last, end := v, s.skipVersion(k)
	// EP01-03, EP01-EP03, E01~E03
	if c := s.at(end); c == '-' || c == '~' {
		p := end + 1
		if s.hasPrefix(p, "ep") {
			p += 2
		} else if s.at(p) == 'e' {
			p++
		}
		if w, p2, ok := s.readNum(p, 4); ok && w > v && s.wordEnd(s.skipVersion(p2)) {
			last, end = w, s.skipVersion(p2)
		}
	}
	if !s.wordEnd(end) {
		return 0, false
	}
	// A leading single-letter "E3 2019 Keynote" is a name, not an episode.
	if plen == 1 && !s.textBetween(s.start, i) && s.textBetween(end, s.n) {
		return 0, false
	}
	s.add(mark{kind: mkEpisode, start: i, end: end, ep: v, epEnd: last, rule: rule, active: true})
	return end, true
}

func validDate(y, m, d int) bool {
	if y < 1900 || y > 2099 || m < 1 || m > 12 || d < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[m-1]
	if m == 2 && (y%4 == 0 && y%100 != 0 || y%400 == 0) {
		days = 29
	}
	return d <= days
}

func (s *segment) matchDigitWord(i int, opts Options) (int, bool) {
	l := s.l
	// Dates: 2024-01-31, 2024.01.31, 2024_01_31, 2024 01 31, 2024年1月31日.
	if y, j, ok := s.readNum(i, 4); ok && j-i == 4 {
		var mo, d, end int
		found := false
		if c := s.at(j); strings.ContainsRune("-._ ", c) && c != 0 {
			if m1, k, ok := s.readNum(j+1, 2); ok && s.at(k) == c {
				if d1, k2, ok := s.readNum(k+1, 2); ok && s.wordEnd(k2) {
					mo, d, end, found = m1, d1, k2, true
				}
			}
		} else if c == '年' {
			if m1, k, ok := s.readNum(s.skipSep(j+1, 1, " "), 2); ok && s.at(k) == '月' {
				if d1, k2, ok := s.readNum(s.skipSep(k+1, 1, " "), 2); ok && s.at(k2) == '日' {
					mo, d, end, found = m1, d1, k2+1, true
				}
			}
		}
		if found && validDate(y, mo, d) {
			if opts.DateEpisodes && s.isFile {
				s.add(mark{kind: mkDate, start: i, end: end, y: y, mo: mo, d: d, rule: "date", active: true})
			} else {
				s.add(mark{kind: mkNoise, start: i, end: end, rule: "date-disabled", active: true})
			}
			return end, true
		}
	}
	// Frame sizes such as 1920x1080 are noise, never season x episode.
	if _, j, ok := s.readNum(i, 4); ok && j-i >= 3 && s.at(j) == 'x' {
		if _, k, ok := s.readNum(j+1, 4); ok && k-j-1 >= 3 && s.wordEnd(k) {
			s.add(mark{kind: mkNoise, start: i, end: k, active: true})
			return k, true
		}
	}
	// 1x01, 1x01-1x03, 1x01-03.
	if season, j, ok := s.readNum(i, 2); ok && s.at(j) == 'x' {
		if ep, k, ok := s.readNum(j+1, 3); ok && k-j-1 >= 2 {
			end, last := s.skipVersion(k), ep
			if s.at(end) == '-' {
				p := end + 1
				if v, p2, ok := s.readNum(p, 2); ok && v == season && s.at(p2) == 'x' {
					p = p2 + 1
				}
				if w, p3, ok := s.readNum(p, 3); ok && p3-p >= 2 && w > ep && s.wordEnd(p3) {
					end, last = p3, w
				}
			}
			if s.wordEnd(end) {
				s.add(mark{kind: mkSE, start: i, end: end, season: season, ep: ep, epEnd: last, rule: "cross", active: true})
				return end, true
			}
		}
	}
	// Ordinal seasons: 2nd Season, 3rd.Season.
	if v, j, ok := s.readNum(i, 2); ok {
		for _, suf := range [...]string{"st", "nd", "rd", "th"} {
			if s.hasPrefix(j, suf) {
				k := s.skipSep(j+2, 2, " ._-")
				if s.hasPrefix(k, "season") && s.wordEnd(k+6) {
					s.add(mark{kind: mkSeason, start: i, end: k + 6, season: v, rule: "season-ordinal", active: true})
					return k + 6, true
				}
			}
		}
	}
	// 01話, 12集.
	if v, j, ok := s.readNum(i, 4); ok && j < s.n && strings.ContainsRune("話话集", l[j]) {
		s.add(mark{kind: mkEpisode, start: i, end: j + 1, ep: v, epEnd: v, rule: "episode-cjk", active: true})
		return j + 1, true
	}
	// Resolution: 720p, 1080p, 1080i, 2160p.
	if _, j, ok := s.readNum(i, 4); ok && j-i >= 3 && (s.at(j) == 'p' || s.at(j) == 'i') && s.wordEnd(j+1) {
		s.add(mark{kind: mkNoise, start: i, end: j + 1, active: true})
		return j + 1, true
	}
	// Year candidates; selection happens in resolve.
	if y, j, ok := s.readNum(i, 4); ok && j-i == 4 && y >= 1900 && y <= 2099 && s.wordEnd(j) {
		m := mark{kind: mkYear, start: i, end: j, y: y, rule: "year-bare"}
		if i > 0 && j < s.n {
			if o, c := s.l[i-1], s.l[j]; (o == '(' || o == '[' || o == '【') && c == closer(o) {
				m.start, m.end, m.paren, m.rule = i-1, j+1, true, "year-paren"
			}
		}
		// A bare year glued to preceding text ("请回答1988") belongs to the title.
		if m.paren || i == s.start || isSep(s.l[i-1]) {
			s.add(m)
			return m.end, true
		}
		return j, true
	}
	// Bare numbers: candidates only.
	if v, j, ok := s.readNum(i, 4); ok {
		end := s.skipVersion(j)
		if !s.wordEnd(end) {
			return 0, false
		}
		m := mark{kind: mkBare, start: i, end: end, ep: v, rule: "bare-plain"}
		if i > 0 && end < s.n {
			if o, c := s.l[i-1], s.l[end]; (o == '[' || o == '【') && c == closer(o) {
				m.style, m.rule = bareBracket, "bare-bracket"
			}
		}
		if m.style == barePlain {
			k, pad := i-1, 0
			for k >= 0 && (s.l[k] == ' ' || s.l[k] == '_') && pad < 2 {
				k, pad = k-1, pad+1
			}
			if k >= 0 && s.l[k] == '-' {
				k2, pad2 := k-1, 0
				for k2 >= 0 && (s.l[k2] == ' ' || s.l[k2] == '_') && pad2 < 2 {
					k2, pad2 = k2-1, pad2+1
				}
				if pad+pad2 > 0 {
					m.style, m.rule = bareDash, "bare-dash"
				}
			}
		}
		s.add(m)
		return end, true
	}
	return 0, false
}
