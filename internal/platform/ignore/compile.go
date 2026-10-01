package ignore

import (
	"context"
	"strings"
)

type pendingSource struct {
	source compiledSource
	text   []byte
}

// Compile snapshots rule values. Input order has no precedence significance;
// deeper reachable sources override ancestors, and lines retain physical order.
func Compile(ctx context.Context, sources []Source, options Options) (program *Program, result error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			program, result = nil, err
		}
		if result != nil {
			program = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Case != CaseSensitive && options.Case != CaseASCIIInsensitive {
		return nil, ErrInvalid
	}
	if len(sources) > MaxSources {
		return nil, ErrLimit
	}
	m := newWorkMeter(ctx, MaxCompileWork)
	p := &Program{mode: options.Case, byBase: make(map[string]int)}
	pending := make([]pendingSource, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	rawTotal := 0
	for _, input := range sources {
		if err := m.check(); err != nil {
			return nil, err
		}
		depth, err := validatePath(input.Path, m)
		if err != nil {
			return nil, err
		}
		if err = m.spend(len(input.Path)); err != nil {
			return nil, err
		}
		slash := strings.LastIndexByte(input.Path, '/')
		base := ""
		name := input.Path
		if slash >= 0 {
			base = input.Path[:slash]
			name = input.Path[slash+1:]
		}
		if name != ".jeleeignore" || seen[input.Path] {
			return nil, ErrInvalid
		}
		seen[input.Path] = true
		if len(input.Text) > MaxSourceBytes || len(input.Text) > MaxTotalSourceBytes-rawTotal {
			return nil, ErrLimit
		}
		rawTotal += len(input.Text)
		// Strings are immutable Go values; Clone prevents retaining a caller's
		// large backing string through a tiny source-path substring.
		path := strings.Clone(input.Path)
		if slash >= 0 {
			base = path[:slash]
		}
		pending = append(pending, pendingSource{source: compiledSource{path: path, base: base, depth: depth - 1}, text: input.Text})
	}
	// Only 128 sources exist. A metered insertion sort makes comparison work
	// and cancellation explicit without callbacks that cannot return errors.
	for i := 1; i < len(pending); i++ {
		v := pending[i]
		j := i
		for j > 0 {
			if err := m.spend(1); err != nil {
				return nil, err
			}
			before := pending[j-1].source
			less := v.source.depth < before.depth
			if v.source.depth == before.depth {
				if err := m.spend(max(len(v.source.path), len(before.path))); err != nil {
					return nil, err
				}
				less = v.source.path < before.path
			}
			if !less {
				break
			}
			pending[j] = pending[j-1]
			j--
		}
		pending[j] = v
	}
	decodedTotal, totalLines := 0, 0
	for _, input := range pending {
		if err := m.check(); err != nil {
			return nil, err
		}
		text, err := decode(input.text, m)
		if err != nil {
			return nil, err
		}
		if len(text) > MaxTotalDecodedBytes-decodedTotal {
			return nil, ErrLimit
		}
		decodedTotal += len(text)
		source := input.source
		source.start = len(p.rules)
		index := len(p.sources)
		p.sources = append(p.sources, source)
		p.byBase[source.base] = index
		line, start := 0, 0
		for cursor := 0; cursor <= len(text); cursor++ {
			if err := m.spend(1); err != nil {
				return nil, err
			}
			if cursor < len(text) && text[cursor] == '\r' && (cursor+1 >= len(text) || text[cursor+1] != '\n') {
				return nil, ErrInvalid
			}
			if cursor < len(text) && text[cursor] != '\n' {
				continue
			}
			if cursor == len(text) && start == cursor {
				break
			}
			line++
			totalLines++
			if line > MaxSourceLines || totalLines > MaxTotalLines {
				return nil, ErrLimit
			}
			end := cursor
			if end > start && text[end-1] == '\r' {
				end--
			}
			if end-start > MaxLineBytes {
				return nil, ErrLimit
			}
			if err := p.compileLine(text[start:end], index, line, m); err != nil {
				return nil, err
			}
			start = cursor + 1
		}
		p.sources[index].end = len(p.rules)
	}
	p.valid = true
	return p, nil
}

func trimTrailingSpaces(line string, m *workMeter) (string, error) {
	last := -1
	for i := 0; i < len(line); i++ {
		if err := m.spend(1); err != nil {
			return "", err
		}
		switch line[i] {
		case ' ':
			if last < 0 {
				last = i
			}
		case '\\':
			i++
			if i == len(line) {
				return line, nil
			}
			if err := m.spend(1); err != nil {
				return "", err
			}
			last = -1
		default:
			last = -1
		}
	}
	if last >= 0 {
		return line[:last], nil
	}
	return line, nil
}

func (p *Program) compileLine(line string, source, number int, m *workMeter) error {
	if err := m.check(); err != nil {
		return err
	}
	line, err := trimTrailingSpaces(line, m)
	if err != nil {
		return err
	}
	if line == "" || line[0] == '#' {
		return nil
	}
	if len(p.rules) >= MaxRules {
		return ErrLimit
	}
	r := rule{source: source, line: number, start: len(p.tokens), basename: true}
	if line[0] == '!' {
		r.negative = true
		line = line[1:]
	}
	if len(line) > 0 && line[len(line)-1] == '/' {
		r.directory = true
		line = line[:len(line)-1]
	}
	for i := 0; i < len(line); i++ {
		if err := m.spend(1); err != nil {
			return err
		}
		if line[i] == '/' {
			r.basename = false
		}
	}
	if len(line) > 0 && line[0] == '/' {
		line = line[1:]
		r.basename = false
	}
	appendToken := func(t matchToken) error {
		if err := m.spend(1); err != nil {
			return err
		}
		if len(p.tokens) >= MaxTokens || len(p.tokens)-r.start >= MaxPatternTokens {
			return ErrLimit
		}
		p.tokens = append(p.tokens, t)
		return nil
	}
	for i := 0; i < len(line); {
		if err := m.spend(1); err != nil {
			return err
		}
		c := line[i]
		t := matchToken{kind: literal, value: c}
		i++
		switch c {
		case '\\':
			if i == len(line) {
				r.invalid = true
				break
			}
			t.value = line[i]
			i++
		case '?':
			t.kind = oneByte
		case '*':
			first := i - 1
			for i < len(line) && line[i] == '*' {
				if err := m.spend(1); err != nil {
					return err
				}
				i++
			}
			t.kind = star
			if i-first >= 2 && (first == 0 || line[first-1] == '/') && (i == len(line) || line[i] == '/' || line[i] == '\\' && i+1 < len(line) && line[i+1] == '/') {
				t.kind = globstar
				if i < len(line) && line[i] == '/' {
					t.kind = componentStar
					i++
				}
			}
		case '[':
			bits, next, valid, err := compileClass(line, i, m, p.mode)
			if err != nil {
				return err
			}
			if !valid {
				r.invalid = true
				break
			}
			if len(p.classes) >= MaxClasses {
				return ErrLimit
			}
			t.kind = byteClass
			t.class = uint16(len(p.classes))
			p.classes = append(p.classes, bits)
			i = next
		}
		if r.invalid {
			break
		}
		if t.kind == literal && p.mode == CaseASCIIInsensitive {
			t.value = fold(t.value)
		}
		if err := appendToken(t); err != nil {
			return err
		}
	}
	r.count = len(p.tokens) - r.start
	if r.invalid {
		p.diagnostics.Total++
		if len(p.diagnostics.Items) < MaxDiagnostics {
			p.diagnostics.Items = append(p.diagnostics.Items, Diagnostic{Source: p.sources[source].path, Line: number, Code: "invalid_pattern"})
		} else {
			p.diagnostics.Truncated = true
		}
	}
	p.maxPattern = max(p.maxPattern, r.count)
	p.rules = append(p.rules, r)
	return nil
}

// Class literals retain Git byte semantics, including its case-fold asymmetry:
// [A] does not match a folded 'a'; [A-Z] and [:upper:] have range/class support.
func compileClass(pattern string, start int, m *workMeter, mode CaseMode) (bitmap, int, bool, error) {
	var bits bitmap
	i := start
	negate := false
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		negate = true
		i++
	}
	first, previous := true, byte(0)
	for i < len(pattern) {
		if err := m.spend(1); err != nil {
			return bitmap{}, 0, false, err
		}
		c := pattern[i]
		i++
		if c == ']' && !first {
			if negate {
				for j := range bits {
					if err := m.spend(1); err != nil {
						return bitmap{}, 0, false, err
					}
					bits[j] = ^bits[j]
				}
			}
			return bits, i, true, nil
		}
		first = false
		if c == '\\' {
			if i == len(pattern) {
				return bitmap{}, 0, false, nil
			}
			c = pattern[i]
			i++
			bits.add(c)
			previous = c
			continue
		}
		if c == '-' && previous != 0 && i < len(pattern) && pattern[i] != ']' {
			end := pattern[i]
			i++
			if end == '\\' {
				if i == len(pattern) {
					return bitmap{}, 0, false, nil
				}
				end = pattern[i]
				i++
			}
			for value := 0; value < 256; value++ {
				if err := m.spend(1); err != nil {
					return bitmap{}, 0, false, err
				}
				b := byte(value)
				if b >= previous && b <= end || mode == CaseASCIIInsensitive && b >= 'a' && b <= 'z' && b-('a'-'A') >= previous && b-('a'-'A') <= end {
					bits.add(b)
				}
			}
			previous = 0
			continue
		}
		if c == '[' && i < len(pattern) && pattern[i] == ':' {
			end := i + 1
			for end < len(pattern) && pattern[end] != ']' {
				if err := m.spend(1); err != nil {
					return bitmap{}, 0, false, err
				}
				end++
			}
			if end == len(pattern) {
				return bitmap{}, 0, false, nil
			}
			if end > i+1 && pattern[end-1] == ':' {
				name := pattern[i+1 : end-1]
				if !knownClass(name) {
					return bitmap{}, 0, false, nil
				}
				for value := 0; value < 128; value++ {
					if err := m.spend(1); err != nil {
						return bitmap{}, 0, false, err
					}
					if namedClass(name, byte(value), mode) {
						bits.add(byte(value))
					}
				}
				i = end + 1
				previous = 0
				continue
			}
		}
		bits.add(c)
		previous = c
	}
	return bitmap{}, 0, false, nil
}

func knownClass(name string) bool {
	switch name {
	case "alnum", "alpha", "blank", "cntrl", "digit", "graph", "lower", "print", "punct", "space", "upper", "xdigit":
		return true
	}
	return false
}
func namedClass(name string, c byte, mode CaseMode) bool {
	lower, upper, digit := c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9'
	switch name {
	case "alnum":
		return lower || upper || digit
	case "alpha":
		return lower || upper
	case "blank":
		return c == ' ' || c == '\t'
	case "cntrl":
		return c < 32 || c == 127
	case "digit":
		return digit
	case "graph":
		return c >= 33 && c <= 126
	case "lower":
		return lower
	case "print":
		return c >= 32 && c <= 126
	case "punct":
		return c >= 33 && c <= 126 && !lower && !upper && !digit
	case "space":
		return c == ' ' || c >= '\t' && c <= '\r'
	case "upper":
		return upper || mode == CaseASCIIInsensitive && lower
	case "xdigit":
		return digit || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	}
	return false
}
