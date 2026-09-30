package probe

// FFprobeFDArguments returns a fresh, fixed argument list for a future isolated
// runner. There is no caller-supplied pathname, format, protocol or output path.
// These restrictions are defense in depth, NOT a filesystem/network sandbox.
// Do not register a media operation until a real OS sandbox has been verified.
// The selected executable must also be checked for these exact capabilities.
func FFprobeFDArguments() []string {
	return []string{
		"-hide_banner", "-v", "error",
		"-max_alloc", "33554432",
		"-threads", "1",
		"-probesize", "1048576",
		"-analyzeduration", "2000000",
		"-max_probe_packets", "2500",
		"-max_streams", "64",
		"-protocol_whitelist", "fd",
		"-format_whitelist", "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,flv,ogg",
		"-enable_drefs", "0",
		"-show_format", "-show_streams", "-show_chapters",
		"-of", "json",
		"-i", "fd:",
	}
}
