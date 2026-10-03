package nfo

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func editDocument(t *testing.T, data []byte) *Document {
	t.Helper()
	d, err := Read(context.Background(), bytes.NewReader(data), DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWithTextPreservesUnknownXMLAndComments(t *testing.T) {
	original := []byte("<?xml version='1.0' encoding='UTF-8'?>\r\n<movie vendor='yes'>\r\n\t<!--lead-->\r\n\t<title custom='x'><![CDATA[old]]><!--inside-->tail</title>\r\n\t<vendor xmlns:x='urn:vendor'><x:title odd='&amp;'>private</x:title></vendor>\r\n\t<uniqueid type='imdb'>tt123</uniqueid>\r\n</movie>\r\n")
	d := editDocument(t, original)
	d.Metadata.Title = "caller edit"
	edited, err := d.WithText(context.Background(), 0, "title", "new < & 中文", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Metadata.Title != "new < & 中文" {
		t.Fatalf("round trip: %q", edited.Metadata.Title)
	}
	want := bytes.Replace(original, []byte("<![CDATA[old]]><!--inside-->tail"), []byte("new &lt; &amp; 中文<!--inside-->"), 1)
	if !bytes.Equal(edited.original, want) {
		t.Fatalf("untouched XML changed: %q", edited.original)
	}
	var copied bytes.Buffer
	if err := d.WriteOriginal(context.Background(), &copied); err != nil || !bytes.Equal(copied.Bytes(), original) {
		t.Fatal("edit mutated original")
	}
}

func TestWithTextWrapperEntriesAndSelfClosing(t *testing.T) {
	for _, tc := range []struct {
		source string
		entry  int
		want   string
	}{
		{`<root><episode><title>a</title></episode><episode><title flag='x'/></episode></root>`, 1, `<root><episode><title>a</title></episode><episode><title flag='x'>changed</title></episode></root>`},
		{`<Item><movie><title><!--keep--></title></movie></Item>`, 0, `<Item><movie><title><!--keep-->changed</title></movie></Item>`},
		{`<episode><title>a</title></episode><episode><title>b</title></episode>`, 1, `<episode><title>a</title></episode><episode><title>changed</title></episode>`},
		{`<season><seasonname>a</seasonname></season>`, 0, `<season><seasonname>changed</seasonname></season>`},
	} {
		d := editDocument(t, []byte(tc.source))
		edited, err := d.WithText(context.Background(), tc.entry, "title", "changed", DefaultMaxBytes)
		if err != nil || string(edited.original) != tc.want {
			t.Fatalf("entry edit: %v", err)
		}
	}
}

func TestWithTextLegacyEncodingAndBOM(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(`<?xml version="1.0" encoding="GBK"?><movie><title>中文</title><!--留存--><extension value='1'>未知</extension></movie>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		gbk,
		encodeUTF16(`<?xml version="1.0" encoding="UTF-16"?><movie><title>中文</title><!--留存--><extension value='1'>未知</extension></movie>`, true, true),
		encodeUTF16(`<?xml version="1.0" encoding="UTF-16"?><movie><title>中文</title><!--留存--><extension value='1'>未知</extension></movie>`, false, true),
		append([]byte{0xef, 0xbb, 0xbf}, []byte(`<movie><title>中文</title><!--留存--><extension value='1'>未知</extension></movie>`)...),
	} {
		d := editDocument(t, data)
		edited, err := d.WithText(context.Background(), 0, "title", "修改 🎬", DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if edited.Encoding != "UTF-8" || edited.Metadata.Title != "修改 🎬" || !bytes.Contains(edited.original, []byte("<!--留存--><extension value='1'>未知</extension>")) {
			t.Fatal("legacy round trip lost retained XML")
		}
		if bytes.HasPrefix(edited.original, []byte{0xef, 0xbb, 0xbf}) != (d.Encoding != "GBK") {
			t.Fatal("BOM policy changed")
		}
	}
}

func TestWithTextRejectsAmbiguityLocksAndInvalidEdits(t *testing.T) {
	for _, tc := range []struct {
		source, field string
		want          error
	}{
		{`<movie><title>a</title><name>b</name></movie>`, "title", ErrEditAmbiguous},
		{`<movie><title>a<extension keep='true'>b</extension></title></movie>`, "title", ErrEditAmbiguous},
		{`<movie><title>a</title><lockdata>true</lockdata></movie>`, "title", ErrEditLocked},
		{`<movie><plot>a</plot><lockedfields>Overview</lockedfields></movie>`, "plot", ErrEditLocked},
		{`<movie><title>a</title><lockedfields>Name</lockedfields></movie>`, "title", ErrEditLocked},
		{`<movie><vendor><title>a</title></vendor></movie>`, "title", ErrEditMissing},
		{`<movie><title>a</title></movie>`, "uniqueid", ErrInvalidInput},
	} {
		d := editDocument(t, []byte(tc.source))
		d.Entries[0].LockedFields = nil
		d.Entries[0].LockData = nil
		if _, err := d.WithText(context.Background(), 0, tc.field, "edited", DefaultMaxBytes); !errors.Is(err, tc.want) {
			t.Fatalf("error %v want %v", err, tc.want)
		}
	}
	d := editDocument(t, []byte(`<movie><title>a</title></movie>`))
	if _, err := d.WithText(context.Background(), 0, "title", "edit", int64(len(d.original)-1)); err != ErrTooLarge {
		t.Fatal("source cap ignored")
	}
	if _, err := d.WithText(context.Background(), 0, "title", "\x00", DefaultMaxBytes); err != ErrInvalidInput {
		t.Fatal("invalid XML text accepted")
	}
	if _, err := d.WithText(context.Background(), 0, "title", strings.Repeat("&", 20), int64(len(d.original)+2)); err != ErrTooLarge {
		t.Fatalf("escaped output cap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.WithText(ctx, 0, "title", "edit", DefaultMaxBytes); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored")
	}
}

func TestWithTextConcurrentCopiesKeepOriginal(t *testing.T) {
	d := editDocument(t, []byte(`<movie><title>original</title><vendor a='b'/></movie>`))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			edited, err := d.WithText(context.Background(), 0, "title", "changed", DefaultMaxBytes)
			if err != nil || edited.Metadata.Title != "changed" {
				t.Errorf("edit: %v", err)
			}
		}()
	}
	wg.Wait()
	if !bytes.Contains(d.original, []byte("<title>original</title>")) {
		t.Fatal("concurrent edits mutated source")
	}
}

func TestWithTextCreatesMissingFieldsAndPreservesLayout(t *testing.T) {
	for _, tc := range []struct {
		source, want, indent string
		entry                int
	}{
		{`<movie vendor='keep'><extension a='x'/></movie>`, `<movie vendor='keep'><extension a='x'/><title>added &amp; new</title></movie>`, "", 0},
		{`<movie vendor='keep'/>`, `<movie vendor='keep'><title>added &amp; new</title></movie>`, "", 0},
		{"<movie>\r\n\t<extension a='x'/>\r\n</movie>", "<movie>\r\n\t<extension a='x'/>\r\n\t<title>added &amp; new</title>\r\n</movie>", "", 0},
		{"<root>\n  <movie>\n    <extension a='x'/>\n  </movie>\n</root>", "<root>\n  <movie>\n    <extension a='x'/>\n    <title>added &amp; new</title>\n  </movie>\n</root>", "", 0},
		{"<movie>\n  <!--keep-->\n</movie>", "<movie>\n  <!--keep-->\n\t<title>added &amp; new</title>\n</movie>", "\t", 0},
		{`<root><episode><title>first</title></episode><episode/></root>`, `<root><episode><title>first</title></episode><episode><title>added &amp; new</title></episode></root>`, "", 1},
	} {
		d := editDocument(t, []byte(tc.source))
		edited, err := d.WithTextOptions(context.Background(), tc.entry, "title", "added & new", DefaultMaxBytes, TextEditOptions{CreateMissing: true, Indent: tc.indent})
		if err != nil {
			t.Fatal(err)
		}
		if string(edited.original) != tc.want || edited.Entries[tc.entry].Title != "added & new" {
			t.Fatalf("layout: %q", edited.original)
		}
	}
}

func TestWithTextBOMOptionsAndMissingFieldLocks(t *testing.T) {
	plain := []byte(`<movie><title>a</title></movie>`)
	for _, include := range []bool{false, true} {
		source := plain
		if include {
			source = append([]byte{0xef, 0xbb, 0xbf}, plain...)
		}
		for _, mode := range []string{"", "preserve", "include", "omit"} {
			d := editDocument(t, source)
			edited, err := d.WithTextOptions(context.Background(), 0, "title", "b", DefaultMaxBytes, TextEditOptions{BOM: mode})
			if err != nil {
				t.Fatal(err)
			}
			want := mode == "include" || mode != "omit" && include
			if bytes.HasPrefix(edited.original, []byte{0xef, 0xbb, 0xbf}) != want {
				t.Fatal("explicit BOM policy ignored")
			}
		}
	}
	d := editDocument(t, []byte(`<movie><title>a</title><lockedfields>Overview</lockedfields></movie>`))
	if _, err := d.WithTextOptions(context.Background(), 0, "plot", "new", DefaultMaxBytes, TextEditOptions{CreateMissing: true}); err != ErrEditLocked {
		t.Fatal("missing field lock bypassed")
	}
	for _, opts := range []TextEditOptions{{BOM: "unknown"}, {Indent: "\n"}, {Indent: strings.Repeat(" ", 9)}} {
		if _, err := d.WithTextOptions(context.Background(), 0, "title", "new", DefaultMaxBytes, opts); err != ErrInvalidInput {
			t.Fatal("invalid formatting option accepted")
		}
	}
}
