package httpapi

import (
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

const itemImagesRoute = "/api/v1/items/{id}/images"

// imageAdminRoutes registers the administrator image API (G40.10, G40.11):
// list a visible item's image rows, upload or replace artwork, delete
// references and lock or unlock one source of a slot. Every route is
// administrator-only and refused with 403 before any lookup.
func (s *Server) imageAdminRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		r.Get(itemImagesRoute, s.accountEndpoint(true, false, s.listItemImages))
		r.Post(itemImagesRoute+"/{type}", s.accountEndpoint(true, true, s.uploadItemImage))
		r.Delete(itemImagesRoute+"/{type}", s.accountEndpoint(true, true, s.deleteItemImages))
		r.Post(itemImagesRoute+"/{type}/lock", s.accountEndpoint(true, true, s.itemImageLock(true)))
		r.Post(itemImagesRoute+"/{type}/unlock", s.accountEndpoint(true, true, s.itemImageLock(false)))
	})
}

// itemImageView never carries a library path or a URL, like the audit rows:
// reference tells a library file from a URL from a stored upload.
type itemImageView struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Index         int        `json:"index"`
	SourceKind    string     `json:"sourceKind"`
	Reference     string     `json:"reference"`
	Locked        bool       `json:"locked"`
	ContentSHA256 string     `json:"contentSha256,omitempty"`
	Format        string     `json:"format,omitempty"`
	Width         int        `json:"width,omitempty"`
	Height        int        `json:"height,omitempty"`
	Bytes         int64      `json:"bytes,omitempty"`
	AverageColor  string     `json:"averageColor,omitempty"`
	DominantColor string     `json:"dominantColor,omitempty"`
	FetchedAt     *time.Time `json:"fetchedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func newItemImageView(value domain.ItemImage) itemImageView {
	view := itemImageView{ID: value.ID, Type: value.Type, Index: value.Index, SourceKind: value.SourceKind, Reference: "store",
		Locked: value.Locked, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	switch {
	case value.RootID != "":
		view.Reference = "file"
	case value.RemoteURL != "":
		view.Reference = "url"
	}
	if c := value.Content; c != nil {
		fetched := c.FetchedAt
		view.ContentSHA256, view.Format, view.Width, view.Height, view.Bytes, view.FetchedAt = hex.EncodeToString(c.SHA256), c.Format, c.Width, c.Height, c.Bytes, &fetched
		view.AverageColor, view.DominantColor = hexColor(c.AverageColor), hexColor(c.DominantColor)
	}
	return view
}

// hexColor writes a 0xRRGGBB color as CSS "#rrggbb"; unknown is empty.
func hexColor(value *int) string {
	if value == nil || *value < 0 || *value > domain.ItemImageMaxAverageColor {
		return ""
	}
	return fmt.Sprintf("#%06x", *value)
}

// imageSlotQuery parses the slot of a request: the type path segment and
// the index, confirm and source query parameters named in keys. index uses
// the same rules as /images.
type imageSlotQuery struct {
	imageType, source string
	index             int
	confirm           bool
}

func parseImageSlotQuery(r *http.Request, keys ...string) (imageSlotQuery, error) {
	query, err := strictQuery(r, keys...)
	if err != nil {
		return imageSlotQuery{}, err
	}
	slot := imageSlotQuery{imageType: chi.URLParam(r, "type")}
	if raw, exists := query["index"]; exists {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || raw != strconv.Itoa(value) || value < 0 || value > domain.ItemImageMaxIndex {
			return imageSlotQuery{}, domain.ErrInvalid
		}
		slot.index = value
	}
	if raw, exists := query["confirm"]; exists {
		if raw != "true" && raw != "false" {
			return imageSlotQuery{}, domain.ErrInvalid
		}
		slot.confirm = raw == "true"
	}
	if raw, exists := query["source"]; exists {
		if !domain.ValidItemImageSourceKind(raw) {
			return imageSlotQuery{}, domain.ErrInvalid
		}
		slot.source = raw
	}
	return slot, nil
}

func (s *Server) listItemImages(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	rows, err := s.images.ItemImages(r.Context(), a, chi.URLParam(r, "id"))
	if err != nil {
		return nil, 0, err
	}
	views := make([]itemImageView, 0, len(rows))
	for _, row := range rows {
		views = append(views, newItemImageView(row))
	}
	return views, http.StatusOK, nil
}

// uploadItemImage takes the raw image as the body. Its declared type and
// length are checked first; its content is verified by the image pipeline,
// never trusted.
func (s *Server) uploadItemImage(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	slot, err := parseImageSlotQuery(r, "index", "confirm")
	if err != nil {
		return nil, 0, err
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/octet-stream" && (len(media) <= len("image/") || media[:len("image/")] != "image/") {
		return nil, 0, domain.ErrImageUnsupported
	}
	if r.ContentLength > s.cfg.Images.MaxSourceBytes {
		return nil, 0, domain.ErrImageTooLarge
	}
	if r.ContentLength < 1 {
		return nil, 0, domain.ErrInvalid
	}
	body := http.MaxBytesReader(w, r.Body, r.ContentLength)
	change, err := s.images.UploadImage(r.Context(), a, chi.URLParam(r, "id"), slot.imageType, slot.index, body, r.ContentLength, slot.confirm)
	if err != nil {
		return nil, 0, err
	}
	status := http.StatusOK
	if change.Created {
		status = http.StatusCreated
	}
	return newItemImageView(change.Image), status, nil
}

func (s *Server) deleteItemImages(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	slot, err := parseImageSlotQuery(r, "index", "confirm", "source")
	if err != nil {
		return nil, 0, err
	}
	if err := emptyAccountInput(w, r); err != nil {
		return nil, 0, err
	}
	_, err = s.images.DeleteImages(r.Context(), a, chi.URLParam(r, "id"), slot.imageType, slot.index, slot.source, slot.confirm)
	return nil, http.StatusNoContent, err
}

func (s *Server) itemImageLock(locked bool) accountOperation {
	return func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		slot, err := parseImageSlotQuery(r, "index", "source")
		if err != nil {
			return nil, 0, err
		}
		if slot.source == "" {
			return nil, 0, domain.ErrInvalid
		}
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		value, err := s.images.SetImageLock(r.Context(), a, chi.URLParam(r, "id"), slot.imageType, slot.index, slot.source, locked)
		if err != nil {
			return nil, 0, err
		}
		return newItemImageView(value), http.StatusOK, nil
	}
}
