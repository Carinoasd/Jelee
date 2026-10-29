package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (f *fakeVariantIndex) DeleteImageVariantsOf(_ context.Context, content [32]byte) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var removed int64
	for id, row := range f.rows {
		if row.ContentSHA256 == content {
			delete(f.rows, id)
			removed++
		}
	}
	return removed, nil
}

func uploadTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal("encode upload fixture")
	}
	return encoded.Bytes()
}

func uploadTestJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal("encode upload fixture")
	}
	return encoded.Bytes()
}

// uploadTestPNGHeader declares a huge image in a valid IHDR chunk without
// the pixels to back it.
func uploadTestPNGHeader(width, height uint32) []byte {
	chunk := make([]byte, 0, 25)
	chunk = binary.BigEndian.AppendUint32(chunk, 13)
	body := []byte("IHDR")
	body = binary.BigEndian.AppendUint32(body, width)
	body = binary.BigEndian.AppendUint32(body, height)
	body = append(body, 8, 6, 0, 0, 0)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
	return append([]byte("\x89PNG\r\n\x1a\n"), append(chunk, make([]byte, 64)...)...)
}

func uploadTestProcessor(t *testing.T) (*Processor, *Store, string, *fakeVariantIndex) {
	t.Helper()
	base := t.TempDir()
	scratch, root := filepath.Join(base, "scratch"), filepath.Join(base, "store")
	for _, dir := range []string{scratch, root} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	store := openTestStore(t, StoreOptions{Root: root, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	index := newFakeVariantIndex()
	options := processorTestOptions(scratch)
	options.Store, options.Index = store, index
	return processorTestNew(t, options), store, root, index
}

func TestImageProcessorIngestsVerifiedUploads(t *testing.T) {
	p, store, _, _ := uploadTestProcessor(t)
	ctx := context.Background()
	for _, c := range []struct {
		format        string
		data          []byte
		width, height int
	}{
		{"png", uploadTestPNG(t, 40, 30), 40, 30},
		{"jpeg", uploadTestJPEG(t, 24, 48), 24, 48},
	} {
		content, err := p.IngestItemImage(ctx, bytes.NewReader(c.data), int64(len(c.data)))
		digest := sha256.Sum256(c.data)
		if err != nil || !bytes.Equal(content.SHA256, digest[:]) || content.Format != c.format || content.Width != c.width ||
			content.Height != c.height || content.Bytes != int64(len(c.data)) || content.FetchedAt.IsZero() {
			t.Fatal("ingest", c.format, err)
		}
		if !store.HasOriginal(digest) {
			t.Fatal("upload not kept in the store", c.format)
		}
		// The stored upload renders like any stored original.
		row := domain.ItemImage{ID: "10000000-0000-4000-8000-0000000000b1", ItemID: "10000000-0000-4000-8000-0000000000b2",
			LibraryID: "10000000-0000-4000-8000-0000000000b3", Type: "Primary", SourceKind: domain.ImageSourceUpload, Locked: true, Content: &content}
		result, err := p.RenderItemImage(ctx, row, domain.ImageRequest{Width: 16})
		if err != nil {
			t.Fatal("render upload", c.format, err)
		}
		if data := assetTestRead(t, result.Body); len(data) == 0 || result.ContentSHA256 != digest {
			t.Fatal("upload representation", c.format)
		}
	}
	if stats := p.Stats(); stats.Active != 0 {
		t.Fatal("ingest kept an admission slot", stats.Active)
	}
}

func TestImageProcessorRejectsUntrustedUploads(t *testing.T) {
	p, store, root, _ := uploadTestProcessor(t)
	ctx := context.Background()
	good := uploadTestPNG(t, 8, 8)
	avif := append([]byte{0, 0, 0, 0x1c}, []byte("ftypavif\x00\x00\x00\x00avifmif1miaf")...)
	cases := []struct {
		name string
		body []byte
		size int64
		want error
	}{
		{"text", []byte("<html>not an image</html>"), 25, domain.ErrImageUnsupported},
		{"truncated png", good[:len(good)-20], int64(len(good) - 20), domain.ErrImageUnsupported},
		{"corrupt png data", append(append([]byte{}, good[:40]...), bytes.Repeat([]byte{0xff}, len(good)-40)...), int64(len(good)), domain.ErrImageUnsupported},
		{"avif", avif, int64(len(avif)), domain.ErrImageUnsupported},
		{"pixel bomb", uploadTestPNGHeader(60000, 60000), int64(len(uploadTestPNGHeader(60000, 60000))), domain.ErrImageTooLarge},
		{"short body", good, int64(len(good) + 1), domain.ErrInvalid},
		{"long body", good, int64(len(good) - 1), domain.ErrInvalid},
		{"empty", nil, 0, domain.ErrInvalid},
		{"above source limit", good, 16<<20 + 1, domain.ErrImageTooLarge},
	}
	for _, c := range cases {
		if _, err := p.IngestItemImage(ctx, bytes.NewReader(c.body), c.size); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	for _, name := range storeFiles(t, root) {
		if strings.HasPrefix(name, "originals/") {
			t.Fatal("a rejected upload reached the store", name)
		}
	}
	if store.Stats().OriginalEntries != 0 {
		t.Fatal("rejected uploads indexed")
	}
	if p.Stats().Active != 0 {
		t.Fatal("rejected upload kept an admission slot")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.IngestItemImage(cancelled, bytes.NewReader(good), int64(len(good))); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled ingest", err)
	}
	// Without a store an upload has nowhere to go.
	plain := processorTestNew(t, processorTestOptions(t.TempDir()))
	if _, err := plain.IngestItemImage(ctx, bytes.NewReader(good), int64(len(good))); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("ingest without a store", err)
	}
	if err := plain.ClearItemImageVariants(ctx, [32]byte{1}); err != nil {
		t.Fatal("clear without a store", err)
	}
}

func TestImageProcessorClearsOnlyGeneratedVariantsOfOneOriginal(t *testing.T) {
	p, store, root, index := uploadTestProcessor(t)
	ctx := context.Background()
	rows := make([]domain.ItemImage, 0, 2)
	for _, data := range [][]byte{uploadTestPNG(t, 64, 64), uploadTestPNG(t, 48, 32)} {
		content, err := p.IngestItemImage(ctx, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		row := domain.ItemImage{ID: "10000000-0000-4000-8000-0000000000c1", ItemID: "10000000-0000-4000-8000-0000000000c2",
			LibraryID: "10000000-0000-4000-8000-0000000000c3", Type: "Primary", SourceKind: domain.ImageSourceUpload, Content: &content}
		for _, width := range []int{16, 24} {
			result, err := p.RenderItemImage(ctx, row, domain.ImageRequest{Width: width})
			if err != nil {
				t.Fatal("render variant", err)
			}
			assetTestRead(t, result.Body)
		}
		rows = append(rows, row)
	}
	if store.Stats().VariantEntries != 4 || index.len() != 4 {
		t.Fatal("variant fixture", store.Stats().VariantEntries, index.len())
	}
	var cleared, kept [32]byte
	copy(cleared[:], rows[0].Content.SHA256)
	copy(kept[:], rows[1].Content.SHA256)
	if err := p.ClearItemImageVariants(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	if store.Stats().VariantEntries != 2 || index.len() != 2 {
		t.Fatal("variants of the cleared original remain", store.Stats().VariantEntries, index.len())
	}
	for _, name := range storeFiles(t, root) {
		if strings.HasPrefix(name, "variants/") && strings.Contains(name, hex.EncodeToString(cleared[:])) {
			t.Fatal("variant file of the cleared original remains", name)
		}
	}
	// Both originals stay; only derived data went.
	for _, digest := range [][32]byte{cleared, kept} {
		object, err := store.OpenOriginal(ctx, digest, true)
		if err != nil {
			t.Fatal("original removed", err)
		}
		_ = object.Close()
	}
	if err := p.ClearItemImageVariants(ctx, cleared); err != nil {
		t.Fatal("clearing twice", err)
	}
}
