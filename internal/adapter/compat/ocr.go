package compat

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ocrSubtitle is a bitmap subtitle that subtitle OCR may derive an SRT
// from (G15.6), with the stream index the derived track has here.
type ocrSubtitle struct {
	index int
	track domain.PlaybackSubtitleTrack
}

// ocrSubtitles numbers every OCR source track after the external files, in
// probe order. Every candidate keeps its number whether or not its text
// exists yet, so a number never moves between PlaybackInfo and the stream
// request.
func ocrSubtitles(source domain.PlaybackSource) []ocrSubtitle {
	next := 0
	for _, v := range source.Video {
		next = max(next, v.Index+1)
	}
	for _, a := range source.Audio {
		next = max(next, a.Index+1)
	}
	for _, s := range source.Subtitles {
		next = max(next, s.Index+1)
	}
	next += len(externalSubtitles(source))
	var out []ocrSubtitle
	for _, track := range source.Subtitles {
		if track.OCRSource {
			out = append(out, ocrSubtitle{index: next, track: track})
			next++
		}
	}
	return out
}

// ocrAvailable reports whether OCR-derived subtitles can be delivered.
func (rt *router) ocrAvailable() bool {
	if !rt.extractionAvailable() {
		return false
	}
	available, ok := rt.opts.Library.Extracted.(interface{ OCRAvailable() bool })
	return ok && available.OCRAvailable()
}

// addOCRStreams lists the SRT derived from each bitmap subtitle whose OCR
// result exists, as an external text stream titled "(OCR)". The lookup is
// the authorized delivery lookup, which also queues missing results; the
// bitmap stream itself stays listed and delivered as it is.
func (rt *router) addOCRStreams(ctx context.Context, principal access.Principal, itemID string, source domain.PlaybackSource, info *mediaSourceInfo) {
	if !rt.ocrAvailable() {
		return
	}
	for _, sub := range ocrSubtitles(source) {
		if _, err := rt.opts.Library.Extracted.ResolveExtracted(ctx, principal, source.ID, media.ExtractedOCRSubtitle, sub.track.Index); err != nil {
			continue
		}
		info.MediaStreams = append(info.MediaStreams, mediaStream{
			Codec: "srt", Language: sub.track.Language, Title: domain.OCRTrackTitle(sub.track), IsForced: sub.track.Forced,
			Type: mediaStreamSubtitle, Index: sub.index, IsExternal: true, IsTextSubtitleStream: true,
			SupportsExternalStream: true, DeliveryMethod: subtitleDeliveryExternal,
			DeliveryURL: subtitleURL(itemID, info.ID, sub.index, "srt"),
		})
	}
}
