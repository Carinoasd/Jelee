package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/cache"
)

func TestOpenAPIHandlerServesCachedSpecification(t *testing.T) {
	cfg := ReferenceConfig()
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(Specification(cfg)); err != nil {
		t.Fatal(err)
	}
	handler := openAPIHandler(cfg)
	var member cache.Stats
	found := false
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("GET", "http://localhost/api/v1/openapi.json", nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || !bytes.Equal(w.Body.Bytes(), want.Bytes()) {
			t.Fatalf("request %d: cached response differs from direct encoding", i)
		}
	}
	// No test in this package runs in parallel, so the last registration is ours.
	for _, named := range cache.Default().Snapshot() {
		if named.Name == openAPICacheName {
			member, found = named.Stats, true
		}
	}
	if !found || member.Entries != 1 || member.Misses != 1 || member.Hits != 2 || member.Bytes != int64(want.Len()) {
		t.Fatalf("openapi cache stats %+v (registered %v)", member, found)
	}
	// Memory pressure empties the entry; the next request rebuilds it.
	cache.Default().Shrink(1)
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "http://localhost/api/v1/openapi.json", nil))
	if !bytes.Equal(w.Body.Bytes(), want.Bytes()) {
		t.Fatal("rebuilt response differs")
	}
}
