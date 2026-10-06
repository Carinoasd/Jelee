package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func metadataBackupFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewMetadataBackupWriter(&buf, MetadataBackupHeader{SchemaVersion: 71, CreatedAt: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ kind, data string }{
		{"library", `{"id":"a","name":"Movies"}`},
		{"user", `{"id":"u","name":"admin"}`},
		{"user", `{"id":"v","name":"kid"}`},
		{"webhook", `{"id":"w"}`},
	} {
		if err = w.Write(r.kind, []byte(r.data)); err != nil {
			t.Fatal(err)
		}
	}
	trailer, err := w.Close()
	if err != nil || trailer.Records != 4 || trailer.Counts["user"] != 2 || len(trailer.SHA256) != 64 {
		t.Fatalf("trailer %+v err %v", trailer, err)
	}
	return buf.Bytes()
}

func readMetadataBackup(data []byte, maxSchema int) ([]string, error) {
	r, _, err := NewMetadataBackupReader(bytes.NewReader(data), maxSchema)
	if err != nil {
		return nil, err
	}
	var kinds []string
	for {
		kind, _, err := r.Next()
		if errors.Is(err, io.EOF) {
			return kinds, nil
		}
		if err != nil {
			return kinds, err
		}
		kinds = append(kinds, kind)
	}
}

func TestMetadataBackupRoundTrip(t *testing.T) {
	data := metadataBackupFixture(t)
	kinds, err := readMetadataBackup(data, 71)
	if err != nil || strings.Join(kinds, ",") != "library,user,user,webhook" {
		t.Fatalf("kinds %v err %v", kinds, err)
	}
}

func TestMetadataBackupWriterRefusesDisorderAndFraming(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewMetadataBackupWriter(&buf, MetadataBackupHeader{SchemaVersion: 71})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Write("user", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ kind, data string }{{"library", `{}`}, {"nope", `{}`}, {"user", `[]`}, {"user", "{\n}"}} {
		if err = w.Write(c.kind, []byte(c.data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s %q accepted", c.kind, c.data)
		}
	}
	if _, err = NewMetadataBackupWriter(&buf, MetadataBackupHeader{SchemaVersion: 70}); !errors.Is(err, ErrInvalid) {
		t.Fatal("schema before the format accepted")
	}
}

func TestMetadataBackupReaderRejectsDamage(t *testing.T) {
	data := metadataBackupFixture(t)
	lines := strings.SplitAfter(string(data), "\n")
	withoutTrailer := strings.Join(lines[:len(lines)-2], "")
	cases := []struct {
		name string
		data string
		want error
	}{
		{"flipped byte", strings.Replace(string(data), "Movies", "Movier", 1), ErrMetadataBackupCorrupt},
		{"no trailer", withoutTrailer, ErrMetadataBackupTruncated},
		{"cut mid line", string(data[:len(data)-10]), ErrMetadataBackupTruncated},
		{"empty", "", ErrMetadataBackupTruncated},
		{"record removed", lines[0] + lines[1] + lines[3] + lines[4] + lines[5], ErrMetadataBackupCorrupt},
		{"records reordered", lines[0] + lines[2] + lines[1] + lines[3] + lines[4] + lines[5], ErrMetadataBackupCorrupt},
		{"after trailer", string(data) + "x", ErrMetadataBackupCorrupt},
		{"unknown kind", lines[0] + `{"kind":"session","data":{}}` + "\n", ErrMetadataBackupCorrupt},
		{"not json", lines[0] + "garbage\n", ErrMetadataBackupCorrupt},
		{"wrong format", strings.Replace(string(data), MetadataBackupFormat, "other", 1), ErrMetadataBackupCorrupt},
		{"newer format", strings.Replace(string(data), `"formatVersion":2`, `"formatVersion":3`, 1), ErrMetadataBackupUnsupported},
		{"older format", strings.Replace(string(data), `"formatVersion":2`, `"formatVersion":0`, 1), ErrMetadataBackupUnsupported},
		{"oversized line", lines[0] + `{"kind":"user","data":{"x":"` + strings.Repeat("a", MetadataBackupMaxLine) + `"}}` + "\n", ErrMetadataBackupCorrupt},
	}
	for _, c := range cases {
		if _, err := readMetadataBackup([]byte(c.data), 71); !errors.Is(err, c.want) {
			t.Errorf("%s: err %v want %v", c.name, err, c.want)
		}
	}
	if _, err := readMetadataBackup(data, 70); !errors.Is(err, ErrMetadataBackupUnsupported) {
		t.Fatal("export from a newer schema accepted")
	}
}

// A format version 1 document, written before collections, playlists and
// the settings kinds existed, is still read.
func TestMetadataBackupReadsFormatVersion1(t *testing.T) {
	header := `{"format":"jelee.metadata","formatVersion":1,"schemaVersion":71,"createdAt":"2026-10-04T00:00:00Z","passwordHashes":false}` + "\n"
	record := `{"kind":"library","data":{"id":"a"}}` + "\n"
	sum := sha256.Sum256([]byte(header + record))
	doc := header + record + `{"kind":"end","records":1,"counts":{"library":1},"sha256":"` + hex.EncodeToString(sum[:]) + `"}` + "\n"
	kinds, err := readMetadataBackup([]byte(doc), 83)
	if err != nil || strings.Join(kinds, ",") != "library" {
		t.Fatalf("version 1: %v %v", kinds, err)
	}
	if MetadataBackupFormatVersion != 2 || MetadataBackupMinFormatVersion != 1 {
		t.Fatal("format versions changed without updating docs/backup-restore.md")
	}
}
