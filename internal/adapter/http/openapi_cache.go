package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/platform/cache"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// openAPICacheName is the fixed metric label for the specification cache.
const openAPICacheName = "http.openapi"

// openAPIHandler serves the encoded specification from a bounded cache. The
// document depends only on the router configuration, never on the caller, so
// one shared entry is safe. deprecations is the server's G49.2 table; without
// one the document has no deprecated operation. Cached bytes are written but never mutated.
func openAPIHandler(cfg config.Config, deprecations ...Deprecation) http.HandlerFunc {
	specs, err := cache.New(cache.Options[struct{}, []byte]{
		MaxEntries: 1, MaxBytes: 4 << 20,
		Size: func(_ struct{}, b []byte) int64 { return int64(cap(b)) },
	})
	if err == nil {
		// Last registration wins, so rebuilding a server replaces the old entry.
		_ = cache.Default().Register(openAPICacheName, specs)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if specs == nil {
			writeJSON(w, http.StatusOK, specification(cfg, deprecations))
			return
		}
		body, ok := specs.Get(struct{}{})
		if !ok {
			var buffer bytes.Buffer
			if err := json.NewEncoder(&buffer).Encode(specification(cfg, deprecations)); err != nil {
				writeJSON(w, http.StatusOK, specification(cfg, deprecations))
				return
			}
			// A tight copy keeps the size estimate equal to the retained bytes.
			body = make([]byte, buffer.Len())
			copy(body, buffer.Bytes())
			specs.Set(struct{}{}, body)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
