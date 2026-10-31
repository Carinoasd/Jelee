package formatsamples

import (
	"bytes"
	"os"
	"os/exec"
)

// SidecarDemuxers is the demuxer list of the external track probe. The
// sandbox package pins SidecarProbeArguments to its sealed argv in a test, so
// a change on either side fails there.
const SidecarDemuxers = "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,ogg,mpeg,aac,ac3,eac3,dts,truehd,mlp,flac,mp3,wav,sup,microdvd"

// SidecarProbeArguments is the ffprobe argument list of the external track
// probe (B4), reading the file from standard input.
func SidecarProbeArguments() []string {
	return []string{"-hide_banner", "-v", "error", "-max_alloc", "33554432", "-threads", "1", "-probesize", "1048576", "-analyzeduration", "2000000", "-max_probe_packets", "2500", "-max_streams", "64", "-protocol_whitelist", "fd", "-format_whitelist", SidecarDemuxers, "-enable_drefs", "0", "-show_format", "-show_streams", "-show_chapters", "-of", "json", "-i", "fd:"}
}

// ProbeJSON runs ffprobe with the sidecar argument list on the file, fed
// through standard input like the probe runtime does, and returns its JSON.
// The caller parses it (probe.ParseJSON); this package must not import the
// probe package, which the sandbox tests depend on.
func ProbeJSON(ffprobe, path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // G304: test-support path chosen by the test
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	command := exec.Command(ffprobe, SidecarProbeArguments()...) //nolint:gosec // G204: test-support tool path, fixed argument list
	command.Stdin = file
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
