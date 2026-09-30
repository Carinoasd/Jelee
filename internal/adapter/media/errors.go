package media

import "errors"

// Errors carry no local path or untrusted request text. The HTTP adapter owns
// their status, localized message, and trace ID representation.
var (
	ErrUnauthenticated      = errors.New("authentication_required")
	ErrPlaybackDenied       = errors.New("web_playback_disabled")
	ErrNotFound             = errors.New("media_not_found")
	ErrTranscodeDisabled    = errors.New("transcode_disabled")
	ErrInvalidRequest       = errors.New("invalid_media_request")
	ErrBusy                 = errors.New("stream_limit_reached")
	ErrMethodNotAllowed     = errors.New("method_not_allowed")
	ErrInvalidRange         = errors.New("invalid_range")
	ErrPreconditionFailed   = errors.New("precondition_failed")
	ErrIO                   = errors.New("media_unavailable")
	ErrLookupTimeout        = errors.New("media_lookup_timeout")
	ErrBodyTooLarge         = errors.New("request_too_large")
	ErrUnsupportedMediaType = errors.New("unsupported_media_type")
)
