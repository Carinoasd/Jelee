package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrEditAmbiguous = errors.New("nfo_edit_ambiguous")
	ErrEditLocked    = errors.New("nfo_edit_locked")
	ErrEditMissing   = errors.New("nfo_edit_field_missing")
)

type textPatch struct {
	start, end int
	value      []byte
}
type editEscapeBuffer struct {
	bytes.Buffer
	max int64
}

func (b *editEscapeBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.max {
		return 0, ErrTooLarge
	}
	return b.Buffer.Write(p)
}

type editTextField struct {
	start, openEnd, closeStart int
	name                       string
	text                       []textPatch
	nested                     bool
}
type editTextFrame struct {
	name                                   xml.Name
	container, children                    bool
	field                                  *editTextField
	fields                                 []*editTextField
	start, openEnd, closeStart, firstChild int
}

type TextEditOptions struct {
	CreateMissing bool
	// BOM is preserve (also the zero value), include, or omit.
	BOM string
	// Indent overrides the relative indentation of newly inserted multiline
	// fields. The zero value infers it from an existing sibling.
	Indent string
}

func editableTextName(name string) string {
	switch name {
	case "title", "name", "localtitle", "seasonname":
		return "title"
	case "sorttitle", "sortname":
		return "sorttitle"
	case "originaltitle", "plot", "outline", "tagline", "showtitle", "status", "mpaa", "certification":
		return name
	}
	return ""
}

// WithText returns a new immutable source document after replacing an existing
// scalar field. Untouched XML is copied lexically, including unknown subtrees,
// attributes and comments. Output is UTF-8, retaining the input's BOM policy.
// It does not write files, create missing fields or authorize read-write mode.
func (d *Document) WithText(ctx context.Context, entry int, field, value string, maxBytes int64) (*Document, error) {
	return d.WithTextOptions(ctx, entry, field, value, maxBytes, TextEditOptions{})
}

// WithTextOptions optionally creates a missing scalar field and controls output
// BOM and insertion indentation. Existing XML keeps its lexical layout.
func (d *Document) WithTextOptions(ctx context.Context, entry int, field, value string, maxBytes int64, options TextEditOptions) (*Document, error) {
	if options.BOM != "" && options.BOM != "preserve" && options.BOM != "include" && options.BOM != "omit" || len(options.Indent) > 8 || strings.Trim(options.Indent, " \t") != "" {
		return nil, ErrInvalidInput
	}
	canonical := editableTextName(field)
	if d == nil || ctx == nil || canonical == "" || canonical != field || entry < 0 || maxBytes < 1 || maxBytes > MaxAllowedBytes || !utf8.ValidString(value) {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(d.original)) > maxBytes || int64(len(value)) > maxBytes {
		return nil, ErrTooLarge
	}
	for i, r := range value {
		if i%(32<<10) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !(r == 9 || r == 10 || r == 13 || r >= 0x20 && r <= 0xd7ff || r >= 0xe000 && r <= 0xfffd || r >= 0x10000 && r <= 0x10ffff) {
			return nil, ErrInvalidInput
		}
	}
	// Public extracted views are mutable. Re-read only the retained original
	// before selecting an entry or making a lock decision.
	base, err := parseOriginal(ctx, d.original)
	if err != nil {
		return nil, err
	}
	if entry >= len(base.Entries) {
		return nil, ErrInvalidInput
	}
	for _, issue := range base.Issues {
		if issue.Severity == "error" {
			return nil, ErrInvalidInput
		}
	}
	m := base.Entries[entry]
	if m.LockData != nil && *m.LockData {
		return nil, ErrEditLocked
	}
	for _, lock := range m.LockedFields {
		lock = strings.ToLower(strings.TrimSpace(lock))
		if lock == "overview" {
			lock = "plot"
		}
		if editableTextName(lock) == field {
			return nil, ErrEditLocked
		}
	}
	data, encoding, _, err := decodeEncoding(d.original)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(contextReader{ctx, bytes.NewReader(data)})
	decoder.CharsetReader = decodedCharset(encoding)
	var stack []*editTextFrame
	var selected []*editTextField
	var selectedRoot *editTextFrame
	nextEntry := 0
	for {
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrInvalidXML
		}
		after := int(decoder.InputOffset())
		switch t := token.(type) {
		case xml.StartElement:
			frame := &editTextFrame{name: t.Name, container: len(stack) == 0, start: before, openEnd: after, firstChild: -1}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				if parent.firstChild < 0 {
					parent.firstChild = before
				}
				if parent.field != nil {
					parent.field.nested = true
				}
				if parent.container && !knownRoot(elementName(parent.name)) && (knownRoot(elementName(t.Name)) || wrapperRoot(elementName(t.Name))) {
					frame.container, parent.children = true, true
				}
				if parent.container && knownRoot(elementName(parent.name)) && editableTextName(elementName(t.Name)) == field {
					frame.field = &editTextField{start: before, openEnd: after, name: t.Name.Local}
					parent.fields = append(parent.fields, frame.field)
				}
			}
			stack = append(stack, frame)
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1].field != nil {
				f := stack[len(stack)-1].field
				f.text = append(f.text, textPatch{start: before, end: after})
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, ErrInvalidXML
			}
			frame := stack[len(stack)-1]
			frame.closeStart = before
			if frame.field != nil {
				frame.field.closeStart = before
			}
			if frame.container && !frame.children {
				if nextEntry == entry {
					if !knownRoot(elementName(frame.name)) {
						return nil, ErrEditAmbiguous
					}
					selected = frame.fields
					selectedRoot = frame
				}
				nextEntry++
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(selected) == 0 && !options.CreateMissing {
		return nil, ErrEditMissing
	}
	if selectedRoot == nil || len(selected) > 1 || len(selected) == 1 && selected[0].nested {
		return nil, ErrEditAmbiguous
	}
	escaped := editEscapeBuffer{max: maxBytes}
	if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
		return nil, err
	}
	var patches []textPatch
	if len(selected) == 0 {
		patches = append(patches, insertTextField(data, selectedRoot, field, escaped.Bytes(), options.Indent))
	} else {
		f := selected[0]
		if bytes.HasSuffix(data[f.start:f.openEnd], []byte("/>")) {
			newField := append([]byte(nil), data[f.start:f.openEnd-2]...)
			newField = append(newField, '>')
			newField = append(newField, escaped.Bytes()...)
			newField = append(newField, []byte("</"+f.name+">")...)
			patches = append(patches, textPatch{f.start, f.openEnd, newField})
		} else if len(f.text) == 0 {
			patches = append(patches, textPatch{f.closeStart, f.closeStart, escaped.Bytes()})
		} else {
			patches = f.text
			patches[0].value = escaped.Bytes()
		}
	}
	// Only the declaration's encoding value changes on legacy input. Vendor
	// XML, quote style, ordering and comments are not reconstructed by Encoder.
	if indices := declarationEncoding.FindSubmatchIndex(data); len(indices) == 4 {
		patches = append(patches, textPatch{indices[2], indices[3], []byte("UTF-8")})
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].start < patches[j].start })
	var output bytes.Buffer
	hasBOM := bytes.HasPrefix(d.original, []byte{0xef, 0xbb, 0xbf}) || bytes.HasPrefix(d.original, []byte{0xff, 0xfe}) || bytes.HasPrefix(d.original, []byte{0xfe, 0xff})
	if options.BOM == "include" || options.BOM != "omit" && hasBOM {
		output.Write([]byte{0xef, 0xbb, 0xbf})
	}
	offset := 0
	for _, p := range patches {
		if p.start < offset {
			return nil, ErrEditAmbiguous
		}
		if int64(output.Len()+p.start-offset+len(p.value)) > maxBytes {
			return nil, ErrTooLarge
		}
		output.Write(data[offset:p.start])
		output.Write(p.value)
		offset = p.end
	}
	if int64(output.Len()+len(data)-offset) > maxBytes {
		return nil, ErrTooLarge
	}
	output.Write(data[offset:])
	result, err := parseOriginal(ctx, output.Bytes())
	if err != nil {
		return nil, err
	}
	result.edited = true
	result.editBaseHash = sha256.Sum256(d.original)
	if d.edited {
		result.editBaseHash = d.editBaseHash
	}
	return result, nil
}

func insertTextField(data []byte, root *editTextFrame, name string, value []byte, relativeIndent string) textPatch {
	field := append([]byte("<"+name+">"), value...)
	field = append(field, []byte("</"+name+">")...)
	if bytes.HasSuffix(data[root.start:root.openEnd], []byte("/>")) {
		body := append([]byte(nil), data[root.start:root.openEnd-2]...)
		body = append(body, '>')
		body = append(body, field...)
		body = append(body, []byte("</"+root.name.Local+">")...)
		return textPatch{root.start, root.openEnd, body}
	}
	line := bytes.LastIndexByte(data[root.openEnd:root.closeStart], '\n')
	if line < 0 {
		return textPatch{root.closeStart, root.closeStart, field}
	}
	line += root.openEnd + 1
	closing := data[line:root.closeStart]
	if len(bytes.Trim(closing, " \t")) != 0 {
		return textPatch{root.closeStart, root.closeStart, field}
	}
	newline := []byte("\n")
	if line >= 2 && data[line-2] == '\r' {
		newline = []byte("\r\n")
	}
	indent := append(append([]byte(nil), closing...), []byte("  ")...)
	if root.firstChild >= 0 {
		begin := bytes.LastIndexByte(data[root.openEnd:root.firstChild], '\n')
		if begin >= 0 {
			candidate := data[root.openEnd+begin+1 : root.firstChild]
			if len(bytes.Trim(candidate, " \t")) == 0 {
				indent = append([]byte(nil), candidate...)
			}
		}
	}
	if relativeIndent != "" {
		indent = append(append([]byte(nil), closing...), []byte(relativeIndent)...)
	}
	insert := append(indent, field...)
	insert = append(insert, newline...)
	return textPatch{line, line, insert}
}
