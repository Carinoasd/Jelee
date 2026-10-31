// Package formatsamples holds the smallest original sample of every external
// subtitle and audio format Jelee recognises (G15.1, G16.1; W10-14), so each
// format can be driven through the same path: scan naming, character set and
// content detection, on-demand probing, playback information (native API
// and compatibility layer) and byte-identical delivery.
//
// No sample is checked in: the text gate (tools/textcheck) allows only UTF-8
// LF text, and these samples are deliberately CRLF, byte-order-marked and in
// legacy encodings. Text subtitles are built in memory from the templates in
// texts.go (UTF-8 and every legacy encoding the detector reports), bitmap
// subtitles (VobSub, PGS) and the hand-made MP3 stream are written byte by
// byte, and the remaining audio formats come from an ffmpeg encoder through
// the argument table in audio.go, which the pinned-tool generator
// (tools/gen-fixtures) and the system-ffmpeg tests share. An encoder that a
// given ffmpeg build lacks is reported as missing, never faked.
//
// The package is imported by tests and by the developer fixture generator
// only; no production package may import it.
package formatsamples
