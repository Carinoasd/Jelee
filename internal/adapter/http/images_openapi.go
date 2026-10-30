package httpapi

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func imageSpecification(paths map[string]any, cfg config.Config) {
	methods := map[string]any{}
	for _, method := range []string{"get", "head"} {
		op := operation("Read authorized item artwork", "200", "304", "400", "401", "403", "404", "408", "413", "415", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["description"] = "Artwork of one item slot (G40.1), encoded as JPEG without enlargement. The source is chosen per G40.10: a locked image first, then a library file, an NFO reference, a fetched remote image and embedded artwork. Remote images are never fetched while serving a request; one whose bytes are not stored yet is skipped. Without a usable image the Primary slot falls back to the poster next to the media file. Omitting both dimensions selects a 640×640 fit; omitting one leaves that direction unconstrained, subject to the configured output limit. Omitting quality selects the configured default. Source and live access are checked again after rendering, including cache hits and conditional requests. Responses are private and revalidated unless tag equals the SHA-256 of the original image, in which case they are cacheable for a year and immutable; Vary: Authorization, Cookie still applies. Byte ranges and legacy compatibility parameters are not supported."
		op["parameters"] = []any{
			map[string]any{"name": "type", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"Primary", "Backdrop", "Fanart", "Logo", "ClearLogo", "Banner", "ClearArt", "Art", "Disc", "Thumb", "Landscape", "Chapter", "Box", "BoxRear", "Menu", "Profile"}}, "description": "Image type. Fanart is an alias of Backdrop."},
			idParameter(),
			map[string]any{"name": "index", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "maximum": 9999, "default": 0}, "description": "Gallery position. Only Backdrop (Fanart) and Chapter accept a non-zero index."},
			map[string]any{"name": "tag", "in": "query", "schema": map[string]any{"type": "string", "pattern": "^[0-9A-Fa-f]{1,128}$"}, "description": "Lowercase hexadecimal SHA-256 of the original image. When it matches, the response is served with Cache-Control: private, max-age=31536000, immutable; otherwise the private revalidated policy applies."},
			map[string]any{"name": "width", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 2048}, "description": "Maximum fitted width. The configured output dimension budget is also enforced."},
			map[string]any{"name": "height", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 2048}, "description": "Maximum fitted height. The configured output dimension budget is also enforced."},
			map[string]any{"name": "quality", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": cfg.Images.DefaultQuality}, "description": "Omit to select the configured JPEG quality."},
			map[string]any{"name": "format", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"jpeg"}, "default": "jpeg"}},
			map[string]any{"name": "If-None-Match", "in": "header", "schema": map[string]any{"type": "string", "maxLength": 4096}},
		}
		op["x-jelee-limits"] = map[string]any{"concurrency": cfg.Images.MaxConcurrent, "timeoutSeconds": cfg.Images.TimeoutSeconds, "maxOutputBytes": cfg.Images.MaxOutputBytes, "maxOutputDimension": cfg.Images.MaxOutputDimension, "defaultQuality": cfg.Images.DefaultQuality}
		responses := op["responses"].(map[string]any)
		headers := map[string]any{"ETag": map[string]any{"schema": map[string]any{"type": "string"}}, "Cache-Control": map[string]any{"schema": map[string]any{"type": "string"}}}
		responses["200"] = map[string]any{"description": "Authorized JPEG representation; HEAD returns its headers without a body.", "headers": headers}
		if method == "get" {
			responses["200"].(map[string]any)["content"] = map[string]any{"image/jpeg": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
		}
		responses["304"] = map[string]any{"description": "The authorized current representation matches If-None-Match. No body.", "headers": headers}
		responses["503"] = map[string]any{"description": "Processing admission or storage is unavailable. Busy responses include Retry-After.", "headers": map[string]any{"Retry-After": map[string]any{"schema": map[string]any{"type": "integer", "minimum": 1}}}}
		methods[method] = op
	}
	paths["/images/{type}/{id}"] = methods
}

// imageAdminSpecification documents the administrator image API (G40.10,
// G40.11) registered by imageAdminRoutes.
func imageAdminSpecification(paths, schemas map[string]any, cfg config.Config) {
	types := []string{"Primary", "Backdrop", "Fanart", "Logo", "ClearLogo", "Banner", "ClearArt", "Art", "Disc", "Thumb", "Landscape", "Chapter", "Box", "BoxRear", "Menu", "Profile"}
	stored := []string{"Primary", "Backdrop", "Logo", "ClearLogo", "Banner", "ClearArt", "Art", "Disc", "Thumb", "Landscape", "Chapter", "Box", "BoxRear", "Menu", "Profile"}
	sources := []string{domain.ImageSourceLocal, domain.ImageSourceNFO, domain.ImageSourceRemote, domain.ImageSourceEmbedded, domain.ImageSourceUpload}
	instant := map[string]any{"type": "string", "format": "date-time"}
	schemas["ItemImage"] = objectSchema(map[string]any{
		"id":            map[string]any{"type": "string", "format": "uuid"},
		"type":          map[string]any{"type": "string", "enum": stored, "description": "Stored type; Fanart is stored as Backdrop."},
		"index":         map[string]any{"type": "integer", "minimum": 0, "maximum": domain.ItemImageMaxIndex},
		"sourceKind":    map[string]any{"type": "string", "enum": sources, "description": "G40.10 order after the lock: local, nfo, remote, embedded, upload."},
		"reference":     map[string]any{"type": "string", "enum": []string{"file", "url", "store"}, "description": "What the row points at: a library file, a URL, or bytes held only in the image store. Paths and URLs are never returned."},
		"locked":        map[string]any{"type": "boolean", "description": "A locked row wins its slot and is never rewritten by a scan or refresh. At most one per slot."},
		"contentSha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "SHA-256 of the stored original, once read or fetched; the immutable tag of /images."},
		"format":        map[string]any{"type": "string", "enum": []string{"jpeg", "png", "webp", "avif", "gif", "bmp", "tiff"}},
		"width":         map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemImageMaxDimension},
		"height":        map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemImageMaxDimension},
		"bytes":         map[string]any{"type": "integer", "format": "int64", "minimum": 1},
		"fetchedAt":     instant, "createdAt": instant, "updatedAt": instant,
	}, "id", "type", "index", "sourceKind", "reference", "locked", "createdAt", "updatedAt")
	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	typeParam := map[string]any{"name": "type", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": types}, "description": "Image type. Fanart is an alias of Backdrop."}
	index := map[string]any{"name": "index", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.ItemImageMaxIndex, "default": 0}, "description": "Gallery position, as for /images. Only Backdrop (Fanart) and Chapter accept a non-zero index."}
	confirm := func(description string) map[string]any {
		return map[string]any{"name": "confirm", "in": "query", "schema": map[string]any{"type": "boolean", "default": false}, "description": description}
	}
	source := func(required bool, description string) map[string]any {
		return map[string]any{"name": "source", "in": "query", "required": required, "schema": map[string]any{"type": "string", "enum": sources}, "description": description}
	}
	admin := func(op map[string]any, description string) map[string]any {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["description"] = "Administrator only; other users get 403 forbidden before any lookup. " + description
		op["responses"].(map[string]any)["404"] = map[string]any{"description": "The item is missing or not visible to the administrator (both answered alike), the slot has no matching row, or image_unavailable."}
		return op
	}
	empty := map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("Empty")}}, "description": "An empty JSON object."}

	list := admin(operation("List the image rows of an item", "200", "400", "401", "403", "404", "408", "503"),
		"Every row of every slot, best source first within a slot (the lock, then G40.10 order). Rows that cannot be served yet (a URL without stored bytes) are included.")
	list["parameters"] = []any{idParameter()}
	list["responses"].(map[string]any)["200"] = map[string]any{"description": "Image rows", "content": data(map[string]any{"type": "array", "items": schemaRef("ItemImage")})}
	paths[itemImagesRoute] = map[string]any{"get": list}

	upload := admin(operation("Upload or replace the artwork of an item slot", "200", "201", "400", "401", "403", "404", "408", "413", "415", "503"),
		"The body is the image itself. It is never trusted: the format is sniffed, the header inspected against the processing memory budget and the whole image decoded before its bytes enter the content-addressed image store. JPEG, PNG, WebP, GIF, BMP and TIFF are accepted; AVIF and anything else is 415 image_unsupported, a body above the source limit 413 image_too_large. The upload becomes the slot's upload row and is locked, so it wins over library, NFO, remote and embedded artwork, which stay as fallbacks. A slot that already has any row is a replacement and needs confirm=true, otherwise 400 confirmation_required and nothing is read or stored. Without a configured image store the request is 404 image_unavailable; a full processing pool is 503 image_busy. Library files are never written or removed. Audited as image.added or image.replaced, plus image.unlocked for a lock handed over. Variants generated from a previous upload that nothing references any more are cleared.")
	upload["parameters"] = []any{idParameter(), typeParam, index, confirm("Required true when the slot already has an image; otherwise 400 confirmation_required.")}
	upload["requestBody"] = map[string]any{"required": true, "description": "Raw image bytes with an image/* or application/octet-stream Content-Type and a Content-Length of at most the configured source limit.",
		"content": map[string]any{"image/*": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}, "application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	upload["x-jelee-limits"] = map[string]any{"maxSourceBytes": cfg.Images.MaxSourceBytes, "timeoutSeconds": cfg.Images.TimeoutSeconds}
	responses := upload["responses"].(map[string]any)
	responses["200"] = map[string]any{"description": "The slot's previous upload was replaced.", "content": data(schemaRef("ItemImage"))}
	responses["201"] = map[string]any{"description": "The slot was empty; the upload is its first row.", "content": data(schemaRef("ItemImage"))}
	responses["503"] = map[string]any{"description": "Processing admission or storage is unavailable. Busy responses include Retry-After.", "headers": map[string]any{"Retry-After": map[string]any{"schema": map[string]any{"type": "integer", "minimum": 1}}}}

	remove := admin(operation("Delete the artwork references of an item slot", "204", "400", "401", "403", "404", "408", "503"),
		"Removes the slot's row of one source, or every row when source is omitted. Always needs confirm=true, otherwise 400 confirmation_required. Only references go: library files and stored originals are kept (G40.11); the variants Jelee generated from content nothing references any more are cleared. A library or NFO image is found again by the next scan. Audited as image.deleted per row.")
	remove["parameters"] = []any{idParameter(), typeParam, index, source(false, "Delete only this source's row; omit for every row of the slot."), confirm("Required true; otherwise 400 confirmation_required.")}
	remove["responses"].(map[string]any)["204"] = map[string]any{"description": "Deleted"}

	paths[itemImagesRoute+"/{type}"] = map[string]any{"post": upload, "delete": remove}

	for _, lock := range []struct {
		path, summary, description string
	}{
		{"/lock", "Lock one source of an item slot", "The row then wins the slot whatever its source, and scans and refreshes never rewrite it (G40.10). The lock is handed over from any other source of the slot. Audited as image.locked, plus image.unlocked for the previous lock."},
		{"/unlock", "Unlock one source of an item slot", "The slot falls back to the G40.10 order. Audited as image.unlocked."},
	} {
		op := admin(operation(lock.summary, "200", "400", "401", "403", "404", "408", "413", "415", "503"), lock.description+" Unchanged when the row already has the requested state.")
		op["parameters"] = []any{idParameter(), typeParam, index, source(true, "The source kind whose row changes.")}
		op["requestBody"] = empty
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "The row after the change", "content": data(schemaRef("ItemImage"))}
		paths[itemImagesRoute+"/{type}"+lock.path] = map[string]any{"post": op}
	}
}
