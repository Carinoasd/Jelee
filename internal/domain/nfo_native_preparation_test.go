package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

// Synthetic canonical records exercise encoding only, not physical ownership.
func nativeReceiptFixture(t testing.TB, platform, mediaKind byte, count int) NFONativePreparationReceipt {
	t.Helper()
	id := func(kind, number byte) [48]byte {
		var v [48]byte
		v[0], v[1], v[2], v[16] = 1, platform, kind, number
		return v
	}
	ancestors := make([][48]byte, count)
	for i := range ancestors {
		ancestors[i] = id(2, byte(i+4))
	}
	v, err := NewNFONativePreparationReceipt(id(2, 1), id(mediaKind, 2), id(1, 3), ancestors)
	if err != nil {
		t.Fatal("canonical observation fixture rejected")
	}
	return v
}

func TestNFONativePreparationReceiptBoundedOwnershipAndPrivacy(t *testing.T) {
	for _, platform := range []byte{1, 2} {
		for _, kind := range []byte{1, 2} {
			for _, count := range []int{1, NFONativePreparationMaxAncestors} {
				v := nativeReceiptFixture(t, platform, kind, count)
				data, err := v.MarshalBinary()
				if err != nil || len(data) != 152+48*count || len(data) > NFONativePreparationReceiptMaxBytes {
					t.Fatal("receipt exceeds bounded encoding")
				}
				parsed, err := ParseNFONativePreparationReceipt(data)
				if err != nil || !parsed.Equal(v) {
					t.Fatal("canonical observation lost during persistence encoding")
				}
				data[16] ^= 0xff
				if !parsed.Equal(v) {
					t.Fatal("parsed observation aliases mutable input")
				}
				exported := v.AncestorIdentities()
				exported[0][16] ^= 0xff
				if !parsed.Equal(v) {
					t.Fatal("ancestor accessor exposed mutable observation")
				}
				cloned := CloneNFONativePreparationReceipt(v)
				cloned.ancestors[0][16] ^= 0xff
				if !parsed.Equal(v) || cloned.Equal(v) {
					t.Fatal("observation clone shares mutable ancestors")
				}
				fresh, _ := NewNFONativePreparationReceipt(v.root, v.media, v.nfoFile, v.ancestors)
				fresh.ancestors[0][16] ^= 0xff
				if !parsed.Equal(v) {
					t.Fatal("constructor shares caller ancestors")
				}
				for _, value := range []any{v, &v} {
					encoded, err := json.Marshal(value)
					if err != nil || string(encoded) != "{}" {
						t.Fatal("native receipt leaked through JSON")
					}
					if fmt.Sprint(value) != "nfo native preparation (redacted)" || fmt.Sprintf("%#v", value) != "nfo native preparation (redacted)" {
						t.Fatal("native receipt leaked through formatting")
					}
				}
			}
		}
	}
}

func TestNFONativePreparationReceiptRejectsMalformedAndInconsistentRecords(t *testing.T) {
	v := nativeReceiptFixture(t, 2, 1, 1)
	good, _ := v.MarshalBinary()
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { return append(b, 0) },
		func(b []byte) []byte { b[0] = 2; return b },
		func(b []byte) []byte { b[1] = 1; return b },
		func(b []byte) []byte { b[2] = 2; return b },
		func(b []byte) []byte { b[3] = 1; return b },
		func(b []byte) []byte { b[4] = 0; return b },
		func(b []byte) []byte { b[4] = 255; b[5] = 255; return b },
		func(b []byte) []byte { b[6] = 1; return b },
		func(b []byte) []byte { b[7] = 1; return b },
		func(b []byte) []byte { b[8+2] = 1; return b },
		func(b []byte) []byte { b[56+1] = 1; return b },
		func(b []byte) []byte { copy(b[56:104], b[104:152]); return b },
		func(b []byte) []byte { b[104+2] = 2; return b },
		func(b []byte) []byte { b[152+2] = 1; return b },
		func(b []byte) []byte { b[152+3] = 1; return b },
	} {
		b := mutate(bytes.Clone(good))
		parsed, err := ParseNFONativePreparationReceipt(b)
		if err != ErrInvalid || !parsed.Empty() {
			t.Fatal("malformed observation accepted or leaked partial result")
		}
	}
	for _, n := range []int{0, 7, 152, 199, NFONativePreparationReceiptMaxBytes + 1, 1 << 20} {
		if v, err := ParseNFONativePreparationReceipt(make([]byte, n)); err != ErrInvalid || !v.Empty() {
			t.Fatal("invalid observation length accepted")
		}
	}
	for _, n := range []int{0, NFONativePreparationMaxAncestors + 1} {
		if v, err := NewNFONativePreparationReceipt(v.root, v.media, v.nfoFile, make([][48]byte, n)); err != ErrInvalid || !v.Empty() {
			t.Fatal("unbounded ancestor constructor accepted")
		}
	}
	if data, err := (NFONativePreparationReceipt{}).MarshalBinary(); err != ErrInvalid || data != nil {
		t.Fatal("missing observation serialized as usable proof")
	}
}

func TestNFOWritePreparationNativeReceiptCloneAndKind(t *testing.T) {
	content := []byte("<movie/>")
	hash := sha256.Sum256(content)
	request := NFOWritePrepareRequest{ItemID: nfoID1, Revision: 1, MaxBytes: NFODefaultSourceBytes}
	scope := NFOItemScope{ItemID: nfoID1, LibraryID: nfoID2, SourceID: nfoID1, RootID: nfoID2, Kind: "Movie", Revision: 1, Generation: 1, RootGeneration: 1, MediaPath: "film.mkv", Source: NFOSource{RootPath: "private", RelativePath: "film.nfo"}}
	p := NFOWritePreparation{Version: NFOWritePreparationVersion, Request: request, Scope: scope, Stamp: NFOStamp{Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:]), FingerprintVersion: NFOFingerprintVersion}, Original: content, Replacement: bytes.Clone(content), NativeObservation: nativeReceiptFixture(t, 2, 1, 1)}
	if ValidateNFOWritePreparation(p) != nil {
		t.Fatal("canonical native recipe rejected")
	}
	copy := CloneNFOWritePreparation(p)
	copy.NativeObservation.ancestors[0][16] ^= 0xff
	if copy.NativeObservation.Equal(p.NativeObservation) {
		t.Fatal("recipe clone shares native observation")
	}
	copy = p
	copy.NativeObservation = nativeReceiptFixture(t, 2, 2, 1)
	if ValidateNFOWritePreparation(copy) != ErrInvalid {
		t.Fatal("file recipe accepted directory media observation")
	}
	copy = p
	copy.Scope.RootGeneration = 0
	if ValidateNFOWritePreparation(copy) != ErrInvalid {
		t.Fatal("native observation accepted missing logical root generation")
	}
	copy.NativeObservation = NFONativePreparationReceipt{}
	if ValidateNFOWritePreparation(copy) != nil {
		t.Fatal("historical missing observation cannot be read")
	}
}

func FuzzParseNFONativePreparationReceipt(f *testing.F) {
	v := nativeReceiptFixture(f, 2, 1, 1)
	data, _ := v.MarshalBinary()
	f.Add(data)
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{255}, NFONativePreparationReceiptMaxBytes+1))
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := ParseNFONativePreparationReceipt(data)
		if err != nil {
			if !v.Empty() {
				t.Fatal("invalid receipt leaked partial observation")
			}
			return
		}
		encoded, err := v.MarshalBinary()
		if err != nil || len(encoded) > NFONativePreparationReceiptMaxBytes || !bytes.Equal(encoded, data) {
			t.Fatal("accepted receipt has noncanonical or unbounded encoding")
		}
	})
}
