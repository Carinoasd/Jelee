package domain

// Charset states of an external text subtitle (G15.4). They decide whether
// a UTF-8 copy may be derived; the original bytes stay the default.
const (
	// SidecarCharsetPending: the file has not been inspected yet.
	SidecarCharsetPending = "pending"
	// SidecarCharsetUndetermined: the file was inspected (it has a
	// fingerprint) but detection was not confident enough to record a
	// charset. Only the original is offered.
	SidecarCharsetUndetermined = "undetermined"
	// SidecarCharsetUTF8: the file already is UTF-8; no copy is needed.
	SidecarCharsetUTF8 = "utf8"
	// SidecarCharsetConvertible: a confidently detected charset other than
	// UTF-8. A UTF-8 copy may be derived on first request; it is still
	// withheld when conversion proves the detection wrong.
	SidecarCharsetConvertible = "convertible"
)

// sidecarConvertibleFormats are the text subtitle formats whose bytes carry
// no charset declaration of their own, so a UTF-8 copy with the same
// contents is the same subtitle. TTML and DFXP are XML documents that may
// declare their encoding, which a copy would contradict; the ".idx" half of
// VobSub is ASCII; ".sub" counts only as MicroDVD text.
var sidecarConvertibleFormats = map[string]bool{"srt": true, "ass": true, "ssa": true, "vtt": true, "webvtt": true, "smi": true, "sami": true, "sub": true}

// SidecarCharsetState returns the charset state of one sidecar file, or ""
// when a UTF-8 copy never applies (audio, bitmap and XML subtitles).
// inspected reports a stored fingerprint, which the bounded inspection
// writes together with the charset it was sure of.
func SidecarCharsetState(kind, format, subtitleKind, charset string, inspected bool) string {
	if kind != SidecarKindSubtitle || !sidecarConvertibleFormats[format] {
		return ""
	}
	if format == "sub" && subtitleKind != SidecarSubtitleMicroDVD && charset == "" {
		// VobSub data, or a ".sub" whose content was not checked yet.
		if subtitleKind == SidecarSubtitleVobSub || inspected {
			return ""
		}
		return SidecarCharsetPending
	}
	switch {
	case charset == "UTF-8":
		return SidecarCharsetUTF8
	case charset != "" && validSidecarCharset(charset):
		return SidecarCharsetConvertible
	case inspected:
		return SidecarCharsetUndetermined
	}
	return SidecarCharsetPending
}
