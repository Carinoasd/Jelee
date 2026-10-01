package nfo

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"strings"
)

const (
	maxDepth    = 64
	maxElements = 50000
	maxEntries  = 128
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// Read retains bounded original bytes and walks XML tokens. Unknown subtrees
// are scanned for XML/security validity but are not retained as an object tree.
// A generic blocking reader must provide its own deadline/cancellation support;
// ReadFile additionally closes its owned file on cancellation.
func Read(ctx context.Context, reader io.Reader, maxBytes int64) (*Document, error) {
	if ctx == nil || reader == nil || maxBytes < 1 || maxBytes > MaxAllowedBytes {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	original, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, maxBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrRead
	}
	if int64(len(original)) > maxBytes {
		return nil, ErrTooLarge
	}
	decoded, encoding, guessed, err := decodeEncoding(original)
	if err != nil {
		return nil, err
	}
	document := &Document{Encoding: encoding, OriginalSize: int64(len(original)), original: original}
	if guessed {
		document.Issues = append(document.Issues, Issue{Severity: "warning", Code: "nfo_encoding_guessed", Field: "encoding", Entry: -1})
	}
	decoder := xml.NewDecoder(contextReader{ctx, bytes.NewReader(decoded)})
	decoder.Strict = true
	decoder.CharsetReader = decodedCharset(encoding)
	parser := xmlParser{ctx: ctx, decoder: decoder, document: document}
	var roots []string
	declarationSeen := false
	for {
		token, err := parser.token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			name := elementName(token.Name)
			if document.Root == "" {
				document.Root = strings.ToLower(token.Name.Local)
			}
			roots = append(roots, name)
			if len(roots) > maxEntries {
				return nil, ErrTooComplex
			}
			if !knownRoot(name) && !wrapperRoot(name) {
				document.Issues = append(document.Issues, Issue{Severity: "warning", Code: "nfo_unknown_root", Field: "root", Entry: len(document.Entries)})
			}
			if err := parser.container(token, 1); err != nil {
				return nil, err
			}
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return nil, ErrInvalidXML
			}
		case xml.EndElement:
			return nil, ErrInvalidXML
		case xml.ProcInst:
			if len(roots) != 0 || declarationSeen || !validDeclaration.Match(bytes.TrimSpace(token.Inst)) {
				return nil, ErrInvalidXML
			}
			declarationSeen = true
		}
	}
	if len(roots) == 0 {
		return nil, ErrInvalidXML
	}
	if len(roots) > 1 {
		for _, root := range roots {
			if root != "episodedetails" && root != "episode" {
				return nil, ErrInvalidXML
			}
		}
	}
	if len(document.Entries) == 0 {
		return nil, ErrInvalidXML
	}
	for i, metadata := range document.Entries {
		document.Issues = append(document.Issues, validateMetadata(metadata, i)...)
	}
	document.Metadata = document.Entries[0]
	return document, nil
}

type xmlParser struct {
	ctx      context.Context
	decoder  *xml.Decoder
	document *Document
	elements int
	tokens   int
}

func (p *xmlParser) token() (xml.Token, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	token, err := p.decoder.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		if p.ctx.Err() != nil {
			return nil, p.ctx.Err()
		}
		return nil, ErrInvalidXML
	}
	p.tokens++
	if p.tokens > 200000 {
		return nil, ErrTooComplex
	}
	switch value := token.(type) {
	case xml.Directive:
		return nil, ErrUnsafeXML
	case xml.ProcInst:
		if value.Target != "xml" {
			return nil, ErrUnsafeXML
		}
		if len(value.Inst)+7 > 1024 {
			return nil, ErrTooComplex
		}
	case xml.StartElement:
		p.elements++
		if p.elements > maxElements || len(value.Attr) > 64 {
			return nil, ErrTooComplex
		}
		seen := make(map[xml.Name]bool, len(value.Attr))
		for _, attribute := range value.Attr {
			if seen[attribute.Name] {
				return nil, ErrInvalidXML
			}
			seen[attribute.Name] = true
		}
	}
	return token, nil
}

func elementName(name xml.Name) string {
	if name.Space != "" {
		return ""
	}
	return strings.ToLower(name.Local)
}

func knownRoot(name string) bool {
	switch name {
	case "movie", "tvshow", "season", "episode", "episodedetails":
		return true
	}
	return false
}

func wrapperRoot(name string) bool {
	switch name {
	case "root", "item", "mediabrowser":
		return true
	}
	return false
}

func (p *xmlParser) container(start xml.StartElement, depth int) error {
	if depth > maxDepth {
		return ErrTooComplex
	}
	metadata := Metadata{Root: strings.ToLower(start.Name.Local)}
	entryIndex := len(p.document.Entries)
	childEntries, fields := false, 0
	for {
		token, err := p.token()
		if err != nil {
			if err == io.EOF {
				return ErrInvalidXML
			}
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			name := elementName(token.Name)
			if !knownRoot(elementName(start.Name)) && (knownRoot(name) || wrapperRoot(name)) {
				childEntries = true
				if err := p.container(token, depth+1); err != nil {
					return err
				}
				continue
			}
			capture := knownField(name)
			node, err := p.element(token, depth+1, capture)
			if err != nil {
				return err
			}
			if capture {
				fields++
				p.document.Issues = append(p.document.Issues, mapField(&metadata, node, entryIndex)...)
			}
		case xml.EndElement:
			if token.Name != start.Name {
				return ErrInvalidXML
			}
			if childEntries {
				if fields != 0 {
					p.document.Issues = append(p.document.Issues, Issue{Severity: "warning", Code: "nfo_wrapper_fields_ignored", Field: "root", Entry: entryIndex})
				}
				return nil
			}
			if len(p.document.Entries) >= maxEntries {
				return ErrTooComplex
			}
			if !knownRoot(elementName(start.Name)) && wrapperRoot(elementName(start.Name)) {
				p.document.Issues = append(p.document.Issues, Issue{Severity: "warning", Code: "nfo_kind_unknown", Field: "root", Entry: entryIndex})
			}
			p.document.Entries = append(p.document.Entries, metadata)
			return nil
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return ErrInvalidXML
			}
		case xml.ProcInst:
			return ErrInvalidXML
		}
	}
}

type element struct {
	name       string
	attributes []xml.Attr
	children   []*element
	content    []elementContent
}

type elementContent struct {
	text  string
	child *element
}

func (p *xmlParser) element(start xml.StartElement, depth int, capture bool) (*element, error) {
	if depth > maxDepth {
		return nil, ErrTooComplex
	}
	var node *element
	if capture {
		node = &element{name: elementName(start.Name), attributes: append([]xml.Attr(nil), start.Attr...)}
	}
	for {
		token, err := p.token()
		if err != nil {
			if err == io.EOF {
				return nil, ErrInvalidXML
			}
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			child, err := p.element(token, depth+1, capture)
			if err != nil {
				return nil, err
			}
			if capture {
				node.children = append(node.children, child)
				node.content = append(node.content, elementContent{child: child})
			}
		case xml.CharData:
			if capture {
				node.content = append(node.content, elementContent{text: string(token)})
			}
		case xml.EndElement:
			if token.Name != start.Name {
				return nil, ErrInvalidXML
			}
			return node, nil
		case xml.ProcInst:
			return nil, ErrInvalidXML
		}
	}
}

func (n *element) value() string {
	var builder strings.Builder
	n.appendText(&builder)
	return strings.TrimSpace(builder.String())
}
func (n *element) appendText(builder *strings.Builder) {
	for _, content := range n.content {
		if content.child != nil {
			content.child.appendText(builder)
		} else {
			builder.WriteString(content.text)
		}
	}
}
func (n *element) attribute(name string) string {
	for _, attribute := range n.attributes {
		if attribute.Name.Space == "" && strings.EqualFold(attribute.Name.Local, name) {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}
func (n *element) child(name string) *element {
	for _, child := range n.children {
		if child.name == name {
			return child
		}
	}
	return nil
}
func (n *element) childValue(name string) string {
	if child := n.child(name); child != nil {
		return child.value()
	}
	return ""
}
