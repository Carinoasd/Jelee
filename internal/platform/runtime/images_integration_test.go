package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func TestImagesRuntimePostgresIntegration(t *testing.T) {
	ctx, store, observer, runtimeDSN, applicationName := metricsIntegrationStore(t)
	scratch := imagesIntegrationScratch(t)
	mediaRoot := t.TempDir()
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal("create production password hasher")
	}
	const secret = "image-fixture-password-2026"
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	adminUser, err := store.BootstrapAdmin(ctx, domain.UserInput{Name: "ImageAdmin", DisplayName: "Image admin", Locale: "en-US", PasswordHash: hash})
	if err != nil || !adminUser.Admin {
		t.Fatal("bootstrap image administrator")
	}
	var library, rootID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO libraries(name) VALUES('Image fixtures') RETURNING id::text`).Scan(&library); err != nil {
		t.Fatal("create image fixture library")
	}
	if err := store.Pool.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, mediaRoot).Scan(&rootID); err != nil {
		t.Fatal("create image fixture root")
	}
	items := make(map[string]string)
	originals := make(map[string][32]byte)
	for _, format := range []string{"png", "jpeg"} {
		dir := filepath.Join(mediaRoot, format)
		if os.Mkdir(dir, 0700) != nil {
			t.Fatal("create image fixture directory")
		}
		picture := image.NewNRGBA(image.Rect(0, 0, 128, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 128; x++ {
				if x >= 32 {
					picture.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y * 3), B: 100, A: 255})
				}
			}
		}
		var encoded bytes.Buffer
		if format == "png" {
			err = png.Encode(&encoded, picture)
		} else {
			err = jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 90})
		}
		if err != nil {
			t.Fatal("encode generated fixture")
		}
		for name, data := range map[string][]byte{"clip.mkv": []byte("Jelee synthetic image anchor\n"), "clip-poster." + format: encoded.Bytes()} {
			path := filepath.Join(dir, name)
			if os.WriteFile(path, data, 0600) != nil {
				t.Fatal("write generated image fixture")
			}
			originals[path] = sha256.Sum256(data)
		}
		var item string
		if err := store.Pool.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, library, "Image "+format).Scan(&item); err != nil {
			t.Fatal("create image fixture item")
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, item, library, rootID, format+"/clip.mkv"); err != nil {
			t.Fatal("bind image fixture source")
		}
		items[format] = item
	}
	values := map[string]string{"JELEE_DATABASE_URL": runtimeDSN, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_IMAGES": "true", "JELEE_IMAGE_TEMP_ROOT": scratch}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil {
		t.Fatal("load image runtime configuration")
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
		t.Fatal("build image runtime")
	}
	if err := application.Start(ctx); err != nil {
		t.Fatal("start image runtime")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if application.Stop(c) != nil {
			t.Error("stop image runtime")
		}
		stopped = true
	}
	t.Cleanup(stop)
	if address == "" || life.closeImages == nil {
		t.Fatal("image lifecycle missing")
	}
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	request := func(method, path, token, etag string, payload []byte, want int) (http.Header, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, address+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal("construct image HTTP request")
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
			t.Fatal("request image HTTP endpoint")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(body) > 2<<20 {
			t.Fatal("bounded image HTTP read")
		}
		if response.StatusCode != want {
			t.Fatalf("image HTTP status=%d want=%d", response.StatusCode, want)
		}
		return response.Header, body
	}
	login := func(name string) domain.SessionGrant {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"name": name, "password": secret, "deviceName": "image acceptance"})
		_, body := request("POST", "/api/v1/auth/login", "", "", payload, 200)
		var result struct {
			Data domain.SessionGrant `json:"data"`
		}
		if json.Unmarshal(body, &result) != nil || result.Data.Token == "" {
			t.Fatal("production image HTTP login")
		}
		return result.Data
	}
	admin := login("ImageAdmin")
	actor := domain.Actor{UserID: admin.User.ID, SessionID: admin.Session.ID, IP: "127.0.0.1"}
	user, _, err := store.CreateUser(ctx, actor, domain.UserInput{Name: "ImageViewer", DisplayName: "Image viewer", Locale: "en-US", PasswordHash: hash}, "image-viewer-create")
	if err != nil {
		t.Fatal("create image viewer")
	}
	viewer := login("ImageViewer")
	pathFor := func(format string) string {
		return "/images/Primary/" + items[format] + "?width=32&height=32&quality=85&format=jpeg"
	}
	request("GET", pathFor("png"), "", "", nil, 401)
	request("GET", pathFor("png"), viewer.Token, "", nil, 404)
	if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, user.ID, library); err != nil {
		t.Fatal("grant fixture image access")
	}
	var pngTag string
	for _, format := range []string{"png", "jpeg"} {
		head, body := request("GET", pathFor(format), viewer.Token, "", nil, 200)
		decoded, err := jpeg.Decode(bytes.NewReader(body))
		if err != nil || decoded.Bounds().Dx() != 32 || decoded.Bounds().Dy() != 16 || head.Get("Content-Type") != "image/jpeg" || !strings.Contains(head.Get("Cache-Control"), "private") {
			t.Fatal("real image resize output")
		}
		if format == "png" {
			r, g, b, _ := decoded.At(2, 8).RGBA()
			if r < 60000 || g < 60000 || b < 60000 {
				t.Fatal("transparent PNG did not composite on white")
			}
			pngTag = head.Get("ETag")
		}
		warmHead, warm := request("GET", pathFor(format), viewer.Token, "", nil, 200)
		if !bytes.Equal(body, warm) || head.Get("ETag") == "" || warmHead.Get("ETag") != head.Get("ETag") {
			t.Fatal("warm image representation changed")
		}
		_, body = request("GET", pathFor(format), viewer.Token, head.Get("ETag"), nil, 304)
		if len(body) != 0 {
			t.Fatal("304 returned body")
		}
		_, body = request("HEAD", pathFor(format), viewer.Token, "", nil, 200)
		if len(body) != 0 {
			t.Fatal("HEAD returned body")
		}
	}
	if _, err := store.Pool.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid AND library_id=$2::uuid`, user.ID, library); err != nil {
		t.Fatal("revoke fixture image access")
	}
	request("GET", pathFor("png"), viewer.Token, pngTag, nil, 404)
	request("GET", pathFor("png"), admin.Token, "", nil, 200)
	for path, want := range originals {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != want {
			t.Fatal("original image or media changed")
		}
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal("image staging was not cleaned")
	}
	stop()
	if life.ctx.Err() == nil {
		t.Fatal("image lifetime was not cancelled")
	}
	var connections int
	if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`, applicationName).Scan(&connections); err != nil || connections != 0 {
		t.Fatal("image runtime retained database connections")
	}
	if response, err := client.Get(address + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("image runtime HTTP still open")
	}
}
