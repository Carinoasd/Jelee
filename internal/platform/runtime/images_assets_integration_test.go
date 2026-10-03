package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func assetIntegrationJPEG(t *testing.T, width, height int, shade uint8) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.RGBA{R: shade, G: uint8(x * 2), B: uint8(y * 3), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal("encode asset fixture")
	}
	return encoded.Bytes()
}

// assetVariantFiles counts the store's variant objects on disk.
func assetVariantFiles(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "variants"), func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal("walk variant store")
	}
	return count
}

func TestImageAssetsRuntimePostgresIntegration(t *testing.T) {
	ctx, store, _, runtimeDSN, _ := metricsIntegrationStore(t)
	scratch := imagesIntegrationScratch(t)
	storeRoot := imagesIntegrationScratch(t)
	mediaRoot := t.TempDir()
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal("create production password hasher")
	}
	const secret = "image-asset-password-2026"
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	if _, err := store.BootstrapAdmin(ctx, domain.UserInput{Name: "AssetAdmin", DisplayName: "Asset admin", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal("bootstrap asset administrator")
	}
	var library, rootID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO libraries(name) VALUES('Asset fixtures') RETURNING id::text`).Scan(&library); err != nil {
		t.Fatal("create asset library")
	}
	if err := store.Pool.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, mediaRoot).Scan(&rootID); err != nil {
		t.Fatal("create asset root")
	}
	originals := map[string][32]byte{}
	write := func(relative string, data []byte) [32]byte {
		t.Helper()
		path := filepath.Join(mediaRoot, filepath.FromSlash(relative))
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, data, 0600) != nil {
			t.Fatal("write asset fixture")
		}
		originals[path] = sha256.Sum256(data)
		return originals[path]
	}
	newItem := func(title, directory string) string {
		t.Helper()
		var item string
		if err := store.Pool.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, library, title).Scan(&item); err != nil {
			t.Fatal("create asset item")
		}
		write(directory+"/clip.mkv", []byte("Jelee synthetic asset anchor\n"))
		if _, err := store.Pool.Exec(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, item, library, rootID, directory+"/clip.mkv"); err != nil {
			t.Fatal("bind asset source")
		}
		return item
	}
	addRow := func(item, imageType string, index int, kind, relative, url string, content []byte, locked bool) {
		t.Helper()
		var size *int64
		var format *string
		var fetched *time.Time
		if content != nil {
			n, f, now := int64(1024), "jpeg", time.Now()
			size, format, fetched = &n, &f, &now
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,format,byte_size,fetched_at,locked)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12,$13)`,
			item, library, imageType, index, kind, map[bool]string{true: rootID}[relative != ""], relative, url, content, format, size, fetched, locked); err != nil {
			t.Fatalf("insert item image %s/%d/%s: %v", imageType, index, kind, err)
		}
	}
	// Item one: a local file for every G40.1 type, extra gallery indexes.
	type slot struct {
		imageType string
		index     int
	}
	slots := []slot{}
	for _, imageType := range []string{"Primary", "Backdrop", "Logo", "ClearLogo", "Banner", "ClearArt", "Art", "Disc", "Thumb", "Landscape", "Chapter", "Box", "BoxRear", "Menu", "Profile"} {
		slots = append(slots, slot{imageType, 0})
	}
	slots = append(slots, slot{"Backdrop", 2}, slot{"Chapter", 1})
	assets := newItem("Asset gallery", "gallery")
	digests := map[slot][32]byte{}
	widths := map[slot]int{}
	for i, s := range slots {
		relative := fmt.Sprintf("gallery/%s-%d.jpg", strings.ToLower(s.imageType), s.index)
		widths[s] = 40 + 2*i
		digests[s] = write(relative, assetIntegrationJPEG(t, widths[s], 20, uint8(10*i)))
		addRow(assets, s.imageType, s.index, domain.ImageSourceLocal, relative, "", nil, false)
	}
	// Item two: a fetched remote image locked over a local file, a remote
	// image never fetched, and an NFO file below a missing local file.
	remoteBytes := assetIntegrationJPEG(t, 50, 50, 250)
	remoteDigest := sha256.Sum256(remoteBytes)
	mixed := newItem("Asset sources", "mixed")
	localDigest := write("mixed/poster.jpg", assetIntegrationJPEG(t, 30, 30, 5))
	addRow(mixed, "Primary", 0, domain.ImageSourceLocal, "mixed/poster.jpg", "", nil, false)
	addRow(mixed, "Primary", 0, domain.ImageSourceRemote, "", "https://image.invalid/locked.jpg", remoteDigest[:], true)
	addRow(mixed, "Logo", 0, domain.ImageSourceRemote, "", "https://image.invalid/never.jpg", nil, false)
	addRow(mixed, "Banner", 0, domain.ImageSourceLocal, "mixed/missing-banner.jpg", "", nil, false)
	nfoDigest := write("mixed/nfo-banner.jpg", assetIntegrationJPEG(t, 44, 22, 77))
	addRow(mixed, "Banner", 0, domain.ImageSourceNFO, "mixed/nfo-banner.jpg", "", nil, false)
	// Item three: no rows, only the poster next to the media file.
	legacy := newItem("Asset fallback", "legacy")
	legacyDigest := write("legacy/clip-poster.jpg", assetIntegrationJPEG(t, 36, 18, 120))
	// The remote image was fetched earlier into the persistent store.
	seed, err := imageadapter.OpenStore(ctx, imageadapter.StoreOptions{Root: storeRoot, MediaRoots: []string{mediaRoot}, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal("seed image store", err)
	}
	if digest, _, err := seed.PutOriginal(ctx, bytes.NewReader(remoteBytes), int64(len(remoteBytes))); err != nil || digest != remoteDigest {
		t.Fatal("seed remote original", err)
	}
	if seed.Close(ctx) != nil {
		t.Fatal("close seed store")
	}

	values := map[string]string{"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_IMAGES": "true",
		"JELEE_IMAGE_TEMP_ROOT": scratch, "JELEE_IMAGE_STORE_ROOT": storeRoot}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil {
		t.Fatal("load asset runtime configuration", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
	address := ""
	life.listen = func(c context.Context, network, _ string) (net.Listener, error) {
		listener, err := (&net.ListenConfig{}).Listen(c, network, "127.0.0.1:0")
		if err == nil {
			address = "http://" + listener.Addr().String()
		}
		return listener, err
	}
	application := newWithLifetime(cfg, logger, life)
	if application.Err() != nil {
		t.Fatal("build asset runtime", application.Err())
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal("start asset runtime", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if application.Stop(c) != nil {
			t.Error("stop asset runtime")
		}
		stopped = true
	}
	t.Cleanup(stop)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	request := func(method, path, token, etag string, payload []byte, want int) (http.Header, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, address+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal("construct asset request")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("request asset endpoint")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(body) > 2<<20 {
			t.Fatal("bounded asset read")
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, response.StatusCode, want)
		}
		return response.Header, body
	}
	login := func(name string) domain.SessionGrant {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"name": name, "password": secret, "deviceName": "asset acceptance"})
		_, body := request("POST", "/api/v1/auth/login", "", "", payload, 200)
		var result struct {
			Data domain.SessionGrant `json:"data"`
		}
		if json.Unmarshal(body, &result) != nil || result.Data.Token == "" {
			t.Fatal("asset login")
		}
		return result.Data
	}
	admin := login("AssetAdmin")
	user, _, err := store.CreateUser(ctx, domain.Actor{UserID: admin.User.ID, SessionID: admin.Session.ID, IP: "127.0.0.1"},
		domain.UserInput{Name: "AssetViewer", DisplayName: "Asset viewer", Locale: "en-US", PasswordHash: hash}, "asset-viewer-create")
	if err != nil {
		t.Fatal("create asset viewer")
	}
	viewer := login("AssetViewer")
	pathOf := func(item string, s slot, extra string) string {
		return fmt.Sprintf("/images/%s/%s?index=%d&width=1000%s", s.imageType, item, s.index, extra)
	}
	request("GET", pathOf(assets, slots[0], ""), viewer.Token, "", nil, 404)
	if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, user.ID, library); err != nil {
		t.Fatal("grant asset access")
	}
	decode := func(body []byte) image.Rectangle {
		t.Helper()
		decoded, err := jpeg.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatal("asset response is not JPEG")
		}
		return decoded.Bounds()
	}
	const immutable = "private, max-age=31536000, immutable"
	for _, s := range slots {
		digest := digests[s]
		tag := hex.EncodeToString(digest[:])
		head, body := request("GET", pathOf(assets, s, ""), viewer.Token, "", nil, 200)
		if bounds := decode(body); bounds.Dx() != widths[s] || bounds.Dy() != 20 || !strings.Contains(head.Get("Cache-Control"), "private") {
			t.Fatalf("%s/%d served the wrong image", s.imageType, s.index)
		}
		tagged, again := request("GET", pathOf(assets, s, "&tag="+tag), viewer.Token, "", nil, 200)
		if !bytes.Equal(body, again) || tagged.Get("Cache-Control") != immutable || tagged.Get("ETag") != head.Get("ETag") {
			t.Fatalf("%s/%d content tag", s.imageType, s.index)
		}
		notModified, empty := request("GET", pathOf(assets, s, "&tag="+tag), viewer.Token, head.Get("ETag"), nil, 304)
		if len(empty) != 0 || notModified.Get("Cache-Control") != immutable {
			t.Fatal("tagged 304")
		}
		if _, empty := request("HEAD", pathOf(assets, s, ""), viewer.Token, "", nil, 200); len(empty) != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	if _, body := request("GET", "/images/Fanart/"+assets+"?index=2&width=1000", viewer.Token, "", nil, 200); decode(body).Dx() != widths[slot{"Backdrop", 2}] {
		t.Fatal("Fanart alias")
	}
	request("GET", "/images/Backdrop/"+assets+"?index=5", viewer.Token, "", nil, 404)
	// The locked remote image is served from the store; the local file below
	// it is not used, and the never-fetched remote Logo is skipped, not fetched.
	head, body := request("GET", "/images/Primary/"+mixed+"?width=1000&tag="+hex.EncodeToString(remoteDigest[:]), viewer.Token, "", nil, 200)
	if decode(body).Dx() != 50 || head.Get("Cache-Control") != immutable {
		t.Fatal("locked stored remote image")
	}
	request("GET", "/images/Primary/"+mixed+"?tag="+hex.EncodeToString(localDigest[:]), viewer.Token, "", nil, 200)
	request("GET", "/images/Logo/"+mixed, viewer.Token, "", nil, 404)
	if _, body := request("GET", "/images/Banner/"+mixed+"?width=1000&tag="+hex.EncodeToString(nfoDigest[:]), viewer.Token, "", nil, 200); decode(body).Dx() != 44 {
		t.Fatal("NFO fallback below a missing local file")
	}
	// Without rows, the Primary slot keeps the poster next to the media file.
	legacyHead, body := request("GET", "/images/Primary/"+legacy+"?width=1000&tag="+hex.EncodeToString(legacyDigest[:]), viewer.Token, "", nil, 200)
	if decode(body).Dx() != 36 || legacyHead.Get("Cache-Control") != immutable {
		t.Fatal("local poster fallback")
	}
	request("GET", "/images/Logo/"+legacy, viewer.Token, "", nil, 404)

	// Every stored variant is indexed in the database and vice versa.
	var indexed int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM image_variants`).Scan(&indexed); err != nil {
		t.Fatal("count variant index")
	}
	if files := assetVariantFiles(t, storeRoot); indexed == 0 || files != indexed {
		t.Fatalf("variant files=%d index=%d", files, indexed)
	}
	for s, digest := range digests {
		name := hex.EncodeToString(digest[:])
		if _, err := os.Stat(filepath.Join(storeRoot, "originals", name[:2], name)); err != nil {
			t.Fatalf("original of %s/%d not stored", s.imageType, s.index)
		}
	}
	// Revocation applies to stored variants, tags and conditional requests.
	etag := head.Get("ETag")
	if _, err := store.Pool.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid`, user.ID); err != nil {
		t.Fatal("revoke asset access")
	}
	request("GET", "/images/Primary/"+mixed+"?width=1000&tag="+hex.EncodeToString(remoteDigest[:]), viewer.Token, etag, nil, 404)
	request("HEAD", pathOf(assets, slots[1], ""), viewer.Token, "", nil, 404)
	request("GET", pathOf(assets, slots[1], ""), admin.Token, "", nil, 200)
	for path, want := range originals {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != want {
			t.Fatal("original image or media changed")
		}
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatal("image staging was not cleaned")
	}
	stop()

	// Eviction goes through the index: a reopened store and the database
	// stay consistent down to empty, and originals are kept.
	reopened, err := imageadapter.OpenStore(ctx, imageadapter.StoreOptions{Root: storeRoot, MediaRoots: []string{mediaRoot}, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal("reopen image store", err)
	}
	defer reopened.Close(context.Background())
	if _, entries, _, _ := reopened.VariantUsage(); entries != indexed {
		t.Fatal("rebuilt store lost variants", entries, indexed)
	}
	removed, err := imageadapter.EvictVariants(ctx, reopened, store, 1<<40, indexed/2)
	if err != nil || removed < indexed-indexed/2 {
		t.Fatal("partial eviction", removed, err)
	}
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM image_variants`).Scan(&indexed); err != nil {
		t.Fatal("count variant index")
	}
	if _, entries, _, _ := reopened.VariantUsage(); entries != indexed || assetVariantFiles(t, storeRoot) != indexed {
		t.Fatal("store and index disagree after eviction", entries, indexed)
	}
	if _, err := imageadapter.EvictVariants(ctx, reopened, store, 0, 0); err != nil {
		t.Fatal("full eviction", err)
	}
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM image_variants`).Scan(&indexed); err != nil || indexed != 0 || assetVariantFiles(t, storeRoot) != 0 {
		t.Fatal("eviction left variants", indexed)
	}
	if !reopened.HasOriginal(remoteDigest) || !reopened.HasOriginal(digests[slots[0]]) {
		t.Fatal("eviction removed originals")
	}
}
