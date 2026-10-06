package logging

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
)

// Marked values of the slow query log and the panic archive (G46.3). Like
// the developer mode values they pass the whitelist only under their own key
// and only when built by these constructors; any other rendering (a plain
// handler, fmt) shows [redacted].

type sqlTemplate struct{ text string }

func (sqlTemplate) String() string { return redacted }

func (sqlTemplate) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

type panicStack struct{ text string }

func (panicStack) String() string { return redacted }

func (panicStack) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// sqlTemplateMax bounds the statement text of the slow query log.
const sqlTemplateMax = 512

var (
	sqlEscapeLiteral  = regexp.MustCompile(`(?s)\b[Ee]'(?:[^'\\]|\\.|'')*'`)
	sqlStringLiteral  = regexp.MustCompile(`(?s)(?:\b(?:[XxBb]|[Uu]&))?'(?:[^']|'')*'`)
	sqlDollarLiteral  = regexp.MustCompile(`(?s)\$([A-Za-z_][A-Za-z0-9_]*)?\$.*?\$([A-Za-z_][A-Za-z0-9_]*)?\$`)
	sqlNumericLiteral = regexp.MustCompile(`(^|[^$A-Za-z0-9_.])[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?\b`)
)

// SQLTemplate marks the statement of a slow query record (key "sql"). The
// statement is reduced to its template: whitespace collapsed, string and
// numeric literals replaced by "?", parameters ($1...) kept as they carry
// no value, cut to 512 bytes. Arguments are never part of it.
func SQLTemplate(statement string) slog.Value {
	return slog.AnyValue(sqlTemplate{text: NormalizeSQL(statement)})
}

// NormalizeSQL returns the template SQLTemplate logs.
func NormalizeSQL(statement string) string {
	text := sqlDollarLiteral.ReplaceAllString(statement, "?")
	text = sqlEscapeLiteral.ReplaceAllString(text, "?")
	text = sqlStringLiteral.ReplaceAllString(text, "?")
	text = sqlNumericLiteral.ReplaceAllString(text, "${1}?")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > sqlTemplateMax {
		cut := sqlTemplateMax
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
		text = text[:cut] + "…"
	}
	return text
}

// Panic stacks: function lines keep the qualified function name without
// its argument words, file lines keep the path below the module or the Go
// root with the line number. Anything else in the dump (argument values,
// goroutine headers with states, absolute directories) is dropped.
var (
	stackFunction = regexp.MustCompile(`^([A-Za-z0-9_./*()\[\]{},-]+)\([^()]*\)$`)
	stackFile     = regexp.MustCompile(`^\t(\S+\.(?:go|s)):([0-9]+)(?: \+0x[0-9a-f]+)?$`)
	stackKeep     = []string{"/internal/", "/cmd/", "/tools/", "/src/"}
)

// stackMaxFrames and stackMaxBytes bound one archived stack.
const (
	stackMaxFrames = 64
	stackMaxBytes  = 8 << 10
)

// PanicStack marks a recovered panic's stack (key "stack"), masked as
// described above, for the panic record of the request boundary or a
// worker.
func PanicStack(stack []byte) slog.Value {
	return slog.AnyValue(panicStack{text: MaskStack(string(stack))})
}

// MaskStack returns the masked stack PanicStack logs.
func MaskStack(stack string) string {
	var b strings.Builder
	frames := 0
	for _, line := range strings.Split(stack, "\n") {
		var out string
		if m := stackFile.FindStringSubmatch(line); m != nil {
			out = "\t" + stackPath(m[1]) + ":" + m[2]
		} else if m := stackFunction.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "goroutine ") {
			if frames == stackMaxFrames {
				b.WriteString("...\n")
				break
			}
			frames++
			out = m[1] + "(...)"
		} else {
			continue
		}
		if b.Len()+len(out)+1 > stackMaxBytes {
			b.WriteString("...\n")
			break
		}
		b.WriteString(out)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// stackPath keeps the part of a source path below the module (internal,
// cmd, tools) or the Go root (src); otherwise only the file name.
func stackPath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	for _, marker := range stackKeep {
		if i := strings.LastIndex(path, marker); i >= 0 {
			return strings.TrimPrefix(path[i:], "/")
		}
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
