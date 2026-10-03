package httpapi

import "github.com/MoYuanCN/Jelee/internal/platform/config"

func imageSpecification(paths map[string]any, cfg config.Config) {
	methods := map[string]any{}
	for _, method := range []string{"get", "head"} {
		op := operation("Read authorized local Primary artwork", "200", "304", "400", "401", "403", "404", "408", "413", "415", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["description"] = "Local Primary artwork only, encoded as JPEG without enlargement. Omitting both dimensions selects a 640×640 fit; omitting one leaves that direction unconstrained, subject to the configured output limit. Omitting quality selects the configured default. Source and live access are checked again after rendering, including cache hits and conditional requests. Responses are private and must be revalidated. Byte ranges, immutable tag URLs and legacy compatibility parameters are not supported."
		op["parameters"] = []any{
			map[string]any{"name": "type", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"Primary"}}},
			idParameter(),
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
