package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestEdgeFingerprintBoundsAndOffset(t *testing.T) {
	for _, size := range []int{0, 1, 65535, 65536, 65537, 131071, 131072, 131073, 300000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := make([]byte, size)
			for index := range data {
				data[index] = byte(index * 31)
			}
			name := writeInput(t, t.TempDir(), "media", data)
			file, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			const savedOffset = 7
			if _, err := file.Seek(savedOffset, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			got, err := edgeFingerprint(context.Background(), file, int64(size))
			if err != nil {
				t.Fatal(err)
			}
			// For <=128 KiB every byte appears exactly once. Larger files sample
			// exactly 64 KiB from each end, never the unsampled middle.
			payload := append([]byte(nil), FingerprintVersion...)
			payload = binary.LittleEndian.AppendUint64(payload, uint64(size))
			if size <= 128<<10 {
				payload = append(payload, data...)
			} else {
				payload = append(payload, data[:64<<10]...)
				payload = append(payload, data[size-(64<<10):]...)
			}
			want := sha256.Sum256(payload)
			if got != hex.EncodeToString(want[:]) {
				t.Fatal("fingerprint does not match the versioned size and edge samples")
			}
			if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != savedOffset {
				t.Fatal("fingerprint changed the shared seek position")
			}
			after, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("fingerprint changed source contents")
			}
		})
	}
}

func TestEdgeFingerprintRejectsIncompleteReadAndCancellation(t *testing.T) {
	name := writeInput(t, t.TempDir(), "media", []byte("small"))
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, size := range []int64{-1, 6, 200000} {
		want := ErrUnavailable
		if size < 0 {
			want = ErrInvalidInput
		}
		if got, err := edgeFingerprint(context.Background(), file, size); got != "" || !errors.Is(err, want) || err.Error() != want.Error() {
			t.Fatalf("size %d: fingerprint=%q error=%v", size, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := edgeFingerprint(ctx, file, 5); got != "" || err != context.Canceled {
		t.Fatal("canceled fingerprint returned a value")
	}
	// The empty-file branch also observes cancellation instead of returning a hash.
	if got, err := edgeFingerprint(ctx, file, 0); got != "" || err != context.Canceled {
		t.Fatal("canceled zero-byte fingerprint returned a value")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := edgeFingerprint(context.Background(), file, 5); got != "" || err != ErrUnavailable {
		t.Fatal("closed input did not fail safely")
	}
}

func TestEdgeFingerprintLargeSparseFileReadsOnlyEdges(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sparse-media")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const size = int64(64) << 20
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("first"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("last"), size-4); err != nil {
		t.Fatal(err)
	}
	got, err := edgeFingerprint(context.Background(), file, size)
	if err != nil || len(got) != 64 {
		t.Fatalf("large sparse fingerprint: %v", err)
	}
	if _, err := file.WriteAt([]byte("changed outside sample"), size/2); err != nil {
		t.Fatal(err)
	}
	after, err := edgeFingerprint(context.Background(), file, size)
	if err != nil || after != got {
		t.Fatal("fingerprint read outside its bounded edge samples")
	}
}
