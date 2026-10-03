package nfo

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const generatedIDFixture = "c6700eb7-09db-473e-96ba-c77266d07c57"

func TestEnsureIDRetainsManualIDsAndMutationCannotBypass(t *testing.T) {
	for _, ids := range []string{
		`<uniqueid type='custom' default='true'>manual</uniqueid>`,
		`<imdbid>tt123</imdbid>`, `<tmdbid>42</tmdbid>`, `<tvdbid>7</tvdbid>`, `<id>tt456</id>`,
		`<uniqueid type='custom'>first</uniqueid><uniqueid type='custom'>second</uniqueid>`,
		`<uniqueid type='jelee'>handwritten</uniqueid>`,
	} {
		original := []byte(`<movie><title>old</title><!--keep-->` + ids + `<vendor x='1'/></movie>`)
		d := editDocument(t, original)
		d.Metadata.UniqueIDs = nil
		d.Entries[0].UniqueIDs = nil
		edited, err := d.EnsureIDValue(context.Background(), 0, generatedIDFixture, DefaultMaxBytes, TextEditOptions{})
		if err != nil || !bytes.Equal(edited.original, original) {
			t.Fatalf("manual ID changed: %v", err)
		}
	}
}

func TestEnsureIDMissingInsertsOnlySelectedEntryAndRetainsLayout(t *testing.T) {
	for _, tc := range []struct {
		source, want string
		entry        int
	}{
		{`<movie vendor='keep'/>`, `<movie vendor='keep'><uniqueid type="jelee">` + generatedIDFixture + `</uniqueid></movie>`, 0},
		{"<movie>\r\n\t<!--keep-->\r\n\t<title>old</title>\r\n</movie>", "<movie>\r\n\t<!--keep-->\r\n\t<title>old</title>\r\n\t<uniqueid type=\"jelee\">" + generatedIDFixture + "</uniqueid>\r\n</movie>", 0},
		{`<root><episode><uniqueid type='custom'>first</uniqueid></episode><episode><extension a='1'/></episode></root>`, `<root><episode><uniqueid type='custom'>first</uniqueid></episode><episode><extension a='1'/><uniqueid type="jelee">` + generatedIDFixture + `</uniqueid></episode></root>`, 1},
		{`<episode><title>a</title></episode><episode><title>b</title></episode>`, `<episode><title>a</title></episode><episode><title>b</title><uniqueid type="jelee">` + generatedIDFixture + `</uniqueid></episode>`, 1},
	} {
		d := editDocument(t, []byte(tc.source))
		edited, err := d.EnsureIDValue(context.Background(), tc.entry, generatedIDFixture, DefaultMaxBytes, TextEditOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if string(edited.original) != tc.want {
			t.Fatalf("layout: %q", edited.original)
		}
		ids := edited.Entries[tc.entry].UniqueIDs
		if len(ids) != 1 || ids[0].Type != "jelee" || ids[0].Value != generatedIDFixture || ids[0].Default {
			t.Fatal("generated ID projection differs")
		}
		again, err := edited.EnsureID(context.Background(), tc.entry, DefaultMaxBytes, TextEditOptions{})
		if err != nil || !bytes.Equal(edited.original, again.original) {
			t.Fatal("replay changed generated ID")
		}
	}
}

func TestEnsureIDGeneratesIndependentUUIDv4AndSupportsLegacyBOM(t *testing.T) {
	d := editDocument(t, []byte(`<movie><title>old</title></movie>`))
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		edited, err := d.EnsureID(context.Background(), 0, DefaultMaxBytes, TextEditOptions{})
		if err != nil {
			t.Fatal(err)
		}
		id := edited.Metadata.UniqueIDs[0].Value
		if !domain.ValidID(id) || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) || seen[id] {
			t.Fatal("invalid or repeated generated UUID")
		}
		seen[id] = true
	}
	legacy := editDocument(t, encodeUTF16(`<?xml version="1.0" encoding="UTF-16"?><movie><title>中文</title><!--留存--></movie>`, true, true))
	edited, err := legacy.EnsureIDValue(context.Background(), 0, generatedIDFixture, DefaultMaxBytes, TextEditOptions{BOM: "omit"})
	if err != nil || edited.Encoding != "UTF-8" || bytes.HasPrefix(edited.original, []byte{0xef, 0xbb, 0xbf}) || !bytes.Contains(edited.original, []byte("<!--留存-->")) {
		t.Fatal("legacy conversion lost retained XML")
	}
}

func TestEnsureIDRejectsLockedDamagedAndInvalidRequests(t *testing.T) {
	for _, lock := range []string{"<lockdata>true</lockdata>", "<lockedfields>ProviderIds</lockedfields>", "<lockedfields>uniqueIds</lockedfields>", "<lockedfields>id</lockedfields>", "<lockedfields>imdbid</lockedfields>"} {
		d := editDocument(t, []byte(`<movie><title>old</title>`+lock+`</movie>`))
		d.Metadata.LockData = nil
		d.Metadata.LockedFields = nil
		d.Entries[0].LockData = nil
		d.Entries[0].LockedFields = nil
		if _, err := d.EnsureID(context.Background(), 0, DefaultMaxBytes, TextEditOptions{}); err != ErrEditLocked {
			t.Fatalf("lock bypass: %v", err)
		}
	}
	for _, data := range []string{`<movie><uniqueid type='custom'></uniqueid></movie>`, `<movie><id/></movie>`, `<movie><runtime>invalid</runtime></movie>`} {
		d := editDocument(t, []byte(data))
		if _, err := d.EnsureID(context.Background(), 0, DefaultMaxBytes, TextEditOptions{}); err != ErrInvalidInput {
			t.Fatal("damaged source repaired without authority")
		}
	}
	d := editDocument(t, []byte(`<movie><title>old</title></movie>`))
	if _, err := d.EnsureIDValue(context.Background(), 0, "not-a-uuid", DefaultMaxBytes, TextEditOptions{}); err != ErrInvalidInput {
		t.Fatal("invalid frozen ID accepted")
	}
	if _, err := d.EnsureID(context.Background(), 0, int64(len(d.original)), TextEditOptions{}); err != ErrTooLarge {
		t.Fatal("generated output ignored cap")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.EnsureID(ctx, 0, DefaultMaxBytes, TextEditOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel ignored")
	}
	if _, err := d.EnsureID(context.Background(), 0, DefaultMaxBytes, TextEditOptions{Indent: "\n"}); err != ErrInvalidInput {
		t.Fatal("invalid layout accepted")
	}
}
