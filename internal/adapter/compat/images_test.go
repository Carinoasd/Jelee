package compat

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// fakeImages serves the slots in available for testMovieID to the users
// that can see the movie library, like the image pipeline.
type fakeImages struct {
	deliveries []ImageDelivery
	actors     []domain.Actor
	available  map[string]bool // "Type/index"
	err        error
	summaries  map[string][]domain.ItemImageSummary
	// summaryCalls records every batched read as user and items.
	summaryCalls [][]string
	summaryErr   error
}

func (f *fakeImages) ServeImage(w http.ResponseWriter, r *http.Request, actor domain.Actor, image ImageDelivery) error {
	f.deliveries = append(f.deliveries, image)
	f.actors = append(f.actors, actor)
	if f.err != nil {
		return f.err
	}
	if image.ItemID != testMovieID || actor.UserID != testUserID && actor.UserID != testAdminID {
		return domain.ErrNotFound
	}
	key := image.Request.Type + "/" + string(rune('0'+image.Request.Index))
	if !f.available[key] {
		return domain.ErrNotFound
	}
	body := "jpeg " + key
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+strings.Repeat("e", 64)+`"`)
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	w.Header().Add("Vary", image.Vary)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
	return nil
}

func (f *fakeImages) ImageSummaries(_ context.Context, userID string, itemIDs []string) (map[string][]domain.ItemImageSummary, error) {
	f.summaryCalls = append(f.summaryCalls, append([]string{userID}, itemIDs...))
	if f.summaryErr != nil {
		return nil, f.summaryErr
	}
	out := map[string][]domain.ItemImageSummary{}
	for _, id := range itemIDs {
		if s, ok := f.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

type imageHarness struct {
	*libraryHarness
	images *fakeImages
}

func newImageHarness(t *testing.T, hidden int) *imageHarness {
	t.Helper()
	images := &fakeImages{available: map[string]bool{"Primary/0": true, "Backdrop/0": true, "Backdrop/2": true, "ClearLogo/0": true, "Thumb/0": true}}
	h := newLibraryHarnessConfig(t, hidden, func(o *LibraryOptions) { o.DirectPlay, o.Images = true, images })
	return &imageHarness{libraryHarness: h, images: images}
}

func testImageDigest(b byte) []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = b
	}
	return d
}

var testImageUpdated = time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)

// movieImageSummaries has a primary image with known size, a clear logo
// (no Logo), a landscape thumb (no Thumb), backdrops 0, 1 and 3 (a gap at
// 2), a chapter image and one Jelee-only type without an upstream name.
func movieImageSummaries() []domain.ItemImageSummary {
	s := func(typ string, index int, digest byte, w, h int) domain.ItemImageSummary {
		v := domain.ItemImageSummary{ItemID: testMovieID, Type: typ, Index: index, ImageID: "70000000-0000-4000-8000-0000000000" + hex.EncodeToString([]byte{digest}), UpdatedAt: testImageUpdated, Width: w, Height: h}
		if digest != 0 {
			v.ContentSHA256 = testImageDigest(digest)
		}
		return v
	}
	unread := s("Landscape", 0, 0, 0, 0)
	unread.ImageID = "70000000-0000-4000-8000-0000000000ff"
	return []domain.ItemImageSummary{
		s("Primary", 0, 0x11, 1000, 1500), s("ClearLogo", 0, 0x22, 800, 310), unread,
		s("Backdrop", 0, 0x30, 1920, 1080), s("Backdrop", 1, 0x31, 1920, 1080), s("Backdrop", 3, 0x33, 1920, 1080),
		s("Chapter", 0, 0x40, 320, 180), s("ClearArt", 0, 0x50, 0, 0),
	}
}

func TestParseImageQuery(t *testing.T) {
	q := func(raw string) browseQuery {
		r := httptest.NewRequest(http.MethodGet, "/x?"+raw, nil)
		return readQuery(r)
	}
	for raw, want := range map[string]ImageDelivery{
		"":                                     {Request: domain.ImageRequest{Format: "jpeg"}},
		"maxWidth=300":                         {Request: domain.ImageRequest{Width: 300, Format: "jpeg"}},
		"MAXHEIGHT=400&width=320":              {Request: domain.ImageRequest{Width: 320, Height: 400, Format: "jpeg"}},
		"width=500&maxWidth=200&fillWidth=300": {Request: domain.ImageRequest{Width: 200, Format: "jpeg"}},
		"fillWidth=400&fillHeight=600":         {Request: domain.ImageRequest{Width: 400, Height: 600, Format: "jpeg"}},
		"maxWidth=3840&maxHeight=0":            {Request: domain.ImageRequest{Width: 2048, Format: "jpeg"}},
		"quality=90":                           {Request: domain.ImageRequest{Quality: 90, Format: "jpeg"}},
		"quality=0":                            {Request: domain.ImageRequest{Format: "jpeg"}},
		"quality=250":                          {Request: domain.ImageRequest{Quality: 100, Format: "jpeg"}},
		"format=Webp":                          {Request: domain.ImageRequest{Format: "jpeg"}},
		"format=jpg&tag=ABCdef0123":            {Request: domain.ImageRequest{Format: "jpeg"}, Tag: "ABCdef0123"},
		"tag=not-a-hex-tag":                    {Request: domain.ImageRequest{Format: "jpeg"}},
		"tag=" + strings.Repeat("a", 129):      {Request: domain.ImageRequest{Format: "jpeg"}},
		"percentPlayed=42.5&unplayedCount=3&blur=10&backgroundColor=%23000&foregroundLayer=x&unknown=1": {Request: domain.ImageRequest{Format: "jpeg"}},
	} {
		got, err := parseImageQuery(q(raw))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: %+v %v, want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"width=-1", "maxWidth=abc", "fillHeight=1.5", "height=99999999999", "quality=-3", "format=jpeg", "format=tiff",
		"percentPlayed=NaN", "percentPlayed=x", "blur=soft", "unplayedCount=-1"} {
		if _, err := parseImageQuery(q(raw)); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestImageRoutesServeThroughPipeline(t *testing.T) {
	h := newImageHarness(t, http.StatusNotFound)
	movie := wire(testMovieID)
	w := h.get("/compat/Items/"+movie+"/Images/Primary?maxWidth=300&quality=80&tag="+strings.Repeat("ab", 32), nativeToken)
	if w.Code != http.StatusOK || w.Body.String() != "jpeg Primary/0" || w.Header().Get("Vary") != "Authorization, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token" {
		t.Fatalf("primary: %d %q %v", w.Code, w.Body, w.Header())
	}
	want := ImageDelivery{ItemID: testMovieID, Request: domain.ImageRequest{Type: "Primary", Width: 300, Quality: 80, Format: "jpeg"}, Tag: strings.Repeat("ab", 32), Vary: imageVary}
	if !reflect.DeepEqual(h.images.deliveries[0], want) || h.images.actors[0] != (domain.Actor{UserID: testUserID, SessionID: testSessionID, IP: testClientIP}) {
		t.Fatalf("delivery %+v actor %+v", h.images.deliveries[0], h.images.actors[0])
	}
	// Letter case of the literal segments and of the type, HEAD, the index
	// in the path or the query, and api_key credentials.
	for target, body := range map[string]string{
		"/compat/items/" + movie + "/images/primary":               "jpeg Primary/0",
		"/compat/Items/" + testMovieID + "/Images/BACKDROP/2":      "jpeg Backdrop/2",
		"/compat/Items/" + movie + "/Images/Backdrop?imageIndex=2": "jpeg Backdrop/2",
		"/compat/Items/" + movie + "/Images/Backdrop/":             "jpeg Backdrop/0",
		"/compat/Items/" + movie + "/Images/Logo":                  "jpeg ClearLogo/0",
		"/compat/Items/" + movie + "/Images/Thumb":                 "jpeg Thumb/0",
	} {
		if w := h.get(target, nativeToken); w.Code != http.StatusOK || w.Body.String() != body {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body)
		}
	}
	if w := h.do(http.MethodHead, "/compat/Items/"+movie+"/Images/Primary?api_key="+nativeToken, nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("head: %d %q", w.Code, w.Body)
	}
	// Logo tries Logo, then ClearLogo; a hard failure is not retried.
	n := len(h.images.deliveries)
	h.get("/compat/Items/"+movie+"/Images/Logo", nativeToken)
	if got := h.images.deliveries[n:]; len(got) != 2 || got[0].Request.Type != "Logo" || got[1].Request.Type != "ClearLogo" {
		t.Fatalf("logo fallback %+v", got)
	}
	h.images.err = domain.ErrImageTooLarge
	n = len(h.images.deliveries)
	assertEmpty(t, "too large", h.get("/compat/Items/"+movie+"/Images/Logo", nativeToken), http.StatusRequestEntityTooLarge)
	if len(h.images.deliveries) != n+1 {
		t.Fatal("a final pipeline error was retried with the next slot")
	}
	for err, status := range map[error]int{domain.ErrImageUnsupported: 415, domain.ErrImageUnavailable: 404, domain.ErrDatabase: 503,
		context.DeadlineExceeded: 503, domain.ErrUnauthenticated: 401, domain.ErrInvalid: 400, io.ErrUnexpectedEOF: 500} {
		h.images.err = err
		w := h.get("/compat/Items/"+movie+"/Images/Primary", nativeToken)
		if w.Code != status || w.Header().Get("Content-Type") == "image/jpeg" {
			t.Fatalf("%v: %d", err, w.Code)
		}
	}
	h.images.err = domain.ErrImageBusy
	if w := h.get("/compat/Items/"+movie+"/Images/Primary", nativeToken); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" || w.Body.Len() != 0 {
		t.Fatalf("busy: %d %v", w.Code, w.Header())
	}
	h.images.err = nil

	// Malformed requests are refused before the pipeline.
	n = len(h.images.deliveries)
	for target, status := range map[string]int{
		"/compat/Items/x/Images/Primary":                          400,
		"/compat/Items/" + movie + "/Images/Poster":               400,
		"/compat/Items/" + movie + "/Images/Fanart":               400,
		"/compat/Items/" + movie + "/Images/0":                    400,
		"/compat/Items/" + movie + "/Images/Backdrop/x":           400,
		"/compat/Items/" + movie + "/Images/Backdrop/-1":          400,
		"/compat/Items/" + movie + "/Images/Primary?width=abc":    400,
		"/compat/Items/" + movie + "/Images/Primary?imageIndex=z": 400,
	} {
		assertGeneric(t, target, h.get(target, nativeToken), status)
	}
	if len(h.images.deliveries) != n {
		t.Fatal("a malformed request reached the pipeline")
	}
	assertEmpty(t, "no token", h.do(http.MethodGet, "/compat/Items/"+movie+"/Images/Primary", nil), http.StatusUnauthorized)
	assertEmpty(t, "web token", h.get("/compat/Items/"+movie+"/Images/Primary", webToken), http.StatusUnauthorized)
	assertEmpty(t, "bearer", h.do(http.MethodGet, "/compat/Items/"+movie+"/Images/Primary", hdr("Authorization", "Bearer "+nativeToken)), http.StatusUnauthorized)
	assertEmpty(t, "origin", h.do(http.MethodGet, "/compat/Items/"+movie+"/Images/Primary?api_key="+nativeToken, hdr("Origin", "https://evil.example")), http.StatusForbidden)
	assertEmpty(t, "post", h.do(http.MethodPost, "/compat/Items/"+movie+"/Images/Primary", authHeader(nativeToken)), http.StatusMethodNotAllowed)
	if len(h.images.deliveries) != n {
		t.Fatal("an unauthenticated request reached the pipeline")
	}
}

// G48.3: an invisible item, a missing item and a slot that cannot exist
// all get the configured hidden status, with identical headers.
func TestImageRoutesHideInvisibleItems(t *testing.T) {
	for _, hidden := range []int{http.StatusNotFound, http.StatusForbidden} {
		h := newImageHarness(t, hidden)
		var answers []*httptest.ResponseRecorder
		for _, target := range []string{
			"/compat/Items/" + wire(testMovieID) + "/Images/Primary?maxWidth=10", // in a library the other user cannot see
			"/compat/Items/" + strings.Repeat("ab", 16) + "/Images/Primary?maxWidth=10",
			"/compat/Items/" + wire(testSeriesID) + "/Images/Backdrop/1",
		} {
			w := h.get(target, testOtherToken)
			assertEmpty(t, target, w, hidden)
			answers = append(answers, w)
		}
		for _, w := range answers[1:] {
			if !reflect.DeepEqual(w.Header(), answers[0].Header()) {
				t.Fatalf("%d: hidden answers differ: %v vs %v", hidden, w.Header(), answers[0].Header())
			}
		}
		// Slots that cannot exist: the same answer, without a lookup.
		n := len(h.images.deliveries)
		for _, target := range []string{"/Images/Screenshot", "/Images/Primary/1", "/Images/Backdrop/10000", "/Images/Primary?imageIndex=3"} {
			assertEmpty(t, target, h.get("/compat/Items/"+wire(testMovieID)+target, nativeToken), hidden)
		}
		if len(h.images.deliveries) != n {
			t.Fatal("an impossible slot reached the pipeline")
		}
		// A visible item without that image looks the same too.
		assertEmpty(t, "no banner", h.get("/compat/Items/"+wire(testMovieID)+"/Images/Banner", nativeToken), hidden)
		if w := h.get("/compat/Items/"+wire(testMovieID)+"/Images/Primary", adminToken); w.Code != http.StatusOK {
			t.Fatalf("admin control: %d", w.Code)
		}
	}
}

// The image guard reads only the documented image members as such. Every
// video transformation parameter is still 409 on the image routes, and on
// every stream route the image members remain transformation requests.
func TestImageGuardStaysSeparateFromStreams(t *testing.T) {
	h := newImageHarness(t, http.StatusNotFound)
	movie := wire(testMovieID)
	for _, query := range []string{"width=320", "maxWidth=320&maxHeight=480", "fillWidth=300&fillHeight=450&quality=90", "Width=1&HEIGHT=1&format=Png", "static=true&maxWidth=10"} {
		if w := h.get("/compat/Items/"+movie+"/Images/Primary?"+query, nativeToken); w.Code != http.StatusOK {
			t.Fatalf("image %q: %d", query, w.Code)
		}
	}
	if h.rejections != 0 {
		t.Fatal("image sizes were rejected as transformations")
	}
	n := len(h.images.deliveries)
	for _, query := range []string{"videoCodec=h264", "maxWidth=10&audioCodec=aac", "maxStreamingBitrate=1", "segmentContainer=ts", "static=false", "transcodingProtocol=hls", "h264-level=40", "videoBitRate=1"} {
		w := h.get("/compat/Items/"+movie+"/Images/Primary?"+query, nativeToken)
		if w.Code != http.StatusConflict {
			t.Fatalf("image %q: %d", query, w.Code)
		}
	}
	// The same image members on a non-GET image request, on stream routes
	// and on PlaybackInfo are still transformation requests.
	for _, req := range []struct{ method, target string }{
		{http.MethodPost, "/compat/Items/" + movie + "/Images/Primary?width=320"},
		{http.MethodGet, "/compat/Videos/" + movie + "/stream?static=true&width=320"},
		{http.MethodGet, "/compat/Videos/" + movie + "/stream.mkv?static=true&maxWidth=320"},
		{http.MethodHead, "/compat/Videos/" + movie + "/stream?static=true&maxHeight=320"},
		{http.MethodGet, "/compat/Audio/" + movie + "/stream?static=true&height=1"},
		{http.MethodGet, "/compat/Items/" + movie + "/PlaybackInfo?maxWidth=320"},
		{http.MethodGet, "/compat/Items/" + movie + "?maxWidth=320"},
	} {
		if w := h.do(req.method, req.target, authHeader(nativeToken)); w.Code != http.StatusConflict {
			t.Fatalf("%s %s: %d", req.method, req.target, w.Code)
		}
	}
	if len(h.images.deliveries) != n {
		t.Fatal("a rejected request reached the pipeline")
	}
	for _, path := range []string{"/Items/x/Images/Primary", "/items/x/images/backdrop/3/", "/Items/x/Images/Primary/0"} {
		if !isImagePath(path) {
			t.Fatalf("%s not an image path", path)
		}
	}
	for _, path := range []string{"/Items/x/Images", "/Items//Images/Primary", "/Items/x/Images/Primary/0/stream", "/Videos/x/Images/Primary", "/Items/x/Images/Primary//"} {
		if isImagePath(path) {
			t.Fatalf("%s taken for an image path", path)
		}
	}
	// The guards themselves.
	r := httptest.NewRequest(http.MethodGet, "/compat/Items/x/Images/Primary?maxWidth=1&width=2&fillHeight=3", nil)
	if media.GuardImage(r) != nil || media.GuardProduction(r) == nil {
		t.Fatal("guards do not separate image sizes")
	}
}

func TestImageRoutesAbsentWithoutPipeline(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	assertEmpty(t, "no pipeline", h.get("/compat/Items/"+wire(testMovieID)+"/Images/Primary", nativeToken), http.StatusNotFound)
	seen := map[string]bool{}
	_ = chi.Walk(newImageHarness(t, http.StatusNotFound).handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	})
	for _, route := range []string{"GET /Items/{itemId}/Images/{imageType}", "HEAD /Items/{itemId}/Images/{imageType}", "GET /Items/{itemId}/Images/{imageType}/{imageIndex}", "HEAD /Items/{itemId}/Images/{imageType}/{imageIndex}"} {
		if !seen[route] {
			t.Fatalf("route %s not walkable", route)
		}
	}
}

type imageTaggedItem struct {
	ID                      string            `json:"Id"`
	Type                    string            `json:"Type"`
	ImageTags               map[string]string `json:"ImageTags"`
	BackdropImageTags       []string          `json:"BackdropImageTags"`
	PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio"`
}

func TestItemImageTags(t *testing.T) {
	h := newImageHarness(t, http.StatusNotFound)
	h.images.summaries = map[string][]domain.ItemImageSummary{testMovieID: movieImageSummaries()}
	h.catalog.page = domain.BrowsePage{Items: []domain.BrowseItem{testMovie, testSeries}, Total: 2}
	checkGolden(t, "library_items_images.json", h.get("/compat/Items?Recursive=true&Fields=PrimaryImageAspectRatio", nativeToken))
	checkGolden(t, "library_item_detail_images.json", h.get("/compat/Items/"+wire(testMovieID), nativeToken))
	// One batched read per listing, for the listed items and the reading user.
	if !reflect.DeepEqual(h.images.summaryCalls, [][]string{{testUserID, testMovieID, testSeriesID}, {testUserID, testMovieID}}) {
		t.Fatalf("summary reads %v", h.images.summaryCalls)
	}
	items := func(target string) []imageTaggedItem {
		t.Helper()
		w := h.get(target, nativeToken)
		var page struct{ Items []imageTaggedItem }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
		return page.Items
	}
	primary := hex.EncodeToString(testImageDigest(0x11))
	movie := items("/compat/Items?Recursive=true")[0]
	if movie.ImageTags["Primary"] != primary || movie.ImageTags["Logo"] != hex.EncodeToString(testImageDigest(0x22)) || len(movie.ImageTags["Thumb"]) != 64 ||
		movie.ImageTags["Art"] == "" || len(movie.ImageTags) != 4 || len(movie.BackdropImageTags) != 2 || movie.PrimaryImageAspectRatio != nil {
		t.Fatalf("default listing %+v", movie)
	}
	// Request members choose the tags, as upstream DtoOptions.
	raw := h.get("/compat/Items?Recursive=true&EnableImages=false", nativeToken).Body.String()
	if strings.Contains(raw, `"ImageTags"`) || !strings.Contains(raw, `"BackdropImageTags":["`) {
		t.Fatalf("images disabled: %s", raw)
	}
	raw = h.get("/compat/Items?Recursive=true&ImageTypeLimit=0", nativeToken).Body.String()
	if !strings.Contains(raw, `"ImageTags":{}`) || strings.Contains(raw, "BackdropImageTags") {
		t.Fatalf("limit 0: %s", raw)
	}
	if m := items("/compat/Items?Recursive=true&ImageTypeLimit=1")[0]; len(m.BackdropImageTags) != 1 || len(m.ImageTags) != 4 {
		t.Fatalf("limit 1: %+v", m)
	}
	if m := items("/compat/Items?Recursive=true&EnableImageTypes=Primary,backdrop")[0]; !reflect.DeepEqual(m.ImageTags, map[string]string{"Primary": primary}) || len(m.BackdropImageTags) != 2 {
		t.Fatalf("enabled types: %+v", m)
	}
	calls := len(h.images.summaryCalls)
	if m := items("/compat/Items?Recursive=true&EnableImages=false&ImageTypeLimit=0")[0]; m.ImageTags != nil || m.BackdropImageTags != nil || len(h.images.summaryCalls) != calls {
		t.Fatalf("nothing asked for still read images: %+v", m)
	}
	for _, target := range []string{"/compat/Items?EnableImages=maybe", "/compat/Items?ImageTypeLimit=-1", "/compat/Items?EnableImageTypes=a-b"} {
		assertGeneric(t, target, h.get(target, nativeToken), http.StatusBadRequest)
	}
	// Ids listings and library folders: folders are never looked up.
	calls = len(h.images.summaryCalls)
	items("/compat/Items?Ids=" + wire(testMovieID) + "," + wire(testLibMovies) + "&IncludeItemTypes=Movie,CollectionFolder")
	if got := h.images.summaryCalls[calls:]; len(got) != 1 || !reflect.DeepEqual(got[0], []string{testUserID, testMovieID}) {
		t.Fatalf("ids listing reads %v", got)
	}
	calls = len(h.images.summaryCalls)
	if v := items("/compat/UserViews"); len(v) != 1 || len(h.images.summaryCalls) != calls {
		t.Fatal("library views read images")
	}
	// An administrator reading as another user reads that user's tags.
	h.get("/compat/Items?Recursive=true&userId="+wire(testOtherID), adminToken)
	if last := h.images.summaryCalls[len(h.images.summaryCalls)-1]; last[0] != testOtherID {
		t.Fatalf("read as %v", last)
	}
	h.images.summaryErr = domain.ErrDatabase
	assertEmpty(t, "summaries unavailable", h.get("/compat/Items?Recursive=true", nativeToken), http.StatusServiceUnavailable)
	h.images.summaryErr = nil

	// Without the image module the tags stay empty and nothing is read.
	plain := newLibraryHarness(t, http.StatusNotFound, true)
	if raw := plain.get("/compat/Items/"+wire(testMovieID), nativeToken).Body.String(); !strings.Contains(raw, `"ImageTags":{},"BackdropImageTags":[]`) || strings.Contains(raw, "AspectRatio") {
		t.Fatalf("without images: %s", raw)
	}
}

func TestItemImageSummaryTag(t *testing.T) {
	read := domain.ItemImageSummary{ItemID: testMovieID, Type: "Primary", ImageID: "x", UpdatedAt: testImageUpdated, ContentSHA256: testImageDigest(0xab)}
	if read.Tag() != strings.Repeat("ab", 32) {
		t.Fatalf("content tag %s", read.Tag())
	}
	mtime, size := int64(5), int64(9)
	unread := domain.ItemImageSummary{ItemID: testMovieID, Type: "Primary", ImageID: "x", UpdatedAt: testImageUpdated, SourceModifiedUnixNano: &mtime, SourceSize: &size}
	tag := unread.Tag()
	if len(tag) != 64 || !validImageTag(tag) || tag != unread.Tag() {
		t.Fatalf("version tag %s", tag)
	}
	seen := map[string]bool{tag: true}
	for name, change := range map[string]func(*domain.ItemImageSummary){
		"updated": func(s *domain.ItemImageSummary) { s.UpdatedAt = s.UpdatedAt.Add(time.Nanosecond) },
		"mtime":   func(s *domain.ItemImageSummary) { v := mtime + 1; s.SourceModifiedUnixNano = &v },
		"size":    func(s *domain.ItemImageSummary) { s.SourceSize = nil },
		"row":     func(s *domain.ItemImageSummary) { s.ImageID = "y" },
		"index":   func(s *domain.ItemImageSummary) { s.Type, s.Index = "Backdrop", 1 },
		"item":    func(s *domain.ItemImageSummary) { s.ItemID = testSeriesID },
	} {
		v := unread
		change(&v)
		if seen[v.Tag()] {
			t.Fatalf("%s: tag did not change", name)
		}
		seen[v.Tag()] = true
	}
	if read.Tag() == tag {
		t.Fatal("content and version tags collide")
	}
}
