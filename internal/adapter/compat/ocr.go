package compat

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ocrSubtitles numbers every OCR source track after the external subtitle
// files: embedded PGS and VobSub tracks in probe order, then external .sup
// files and paired VobSub .idx files (domain.UpstreamOCRSlots). Every
// candidate keeps its number whether or not its text exists yet, so a
// number never moves between PlaybackInfo and the stream request.
func ocrSubtitles(source domain.PlaybackSource) []domain.UpstreamOCRSlot {
	return domain.UpstreamOCRSlots(source)
}

// ocrAvailable reports whether OCR-derived subtitles can be delivered.
func (rt *router) ocrAvailable() bool {
	if !rt.extractionAvailable() {
		return false
	}
	available, ok := rt.opts.Library.Extracted.(interface{ OCRAvailable() bool })
	return ok && available.OCRAvailable()
}

// sidecarOCR returns the external bitmap subtitle OCR lookup, or nil when
// the extraction resolver has none.
func (rt *router) sidecarOCR() media.SidecarOCRResolver {
	resolver, _ := rt.opts.Library.Extracted.(media.SidecarOCRResolver)
	return resolver
}

// resolveOCR asks for the derived SRT of one OCR candidate through the
// authorized delivery lookup, which also queues a missing result.
func (rt *router) resolveOCR(ctx context.Context, principal access.Principal, sourceID string, slot domain.UpstreamOCRSlot) error {
	if slot.External != nil {
		resolver := rt.sidecarOCR()
		if resolver == nil {
			return media.ErrNotFound
		}
		_, err := resolver.ResolveSidecarOCR(ctx, principal, sourceID, slot.External.ID)
		return err
	}
	_, err := rt.opts.Library.Extracted.ResolveExtracted(ctx, principal, sourceID, media.ExtractedOCRSubtitle, slot.Embedded.Index)
	return err
}

// addOCRStreams lists the SRT derived from each bitmap subtitle whose OCR
// result exists, as an external text stream titled "(OCR)". The lookup is
// the authorized delivery lookup, which also queues missing results; the
// bitmap stream or external file itself stays listed and delivered as it
// is.
func (rt *router) addOCRStreams(ctx context.Context, principal access.Principal, itemID string, source domain.PlaybackSource, info *mediaSourceInfo) {
	if !rt.ocrAvailable() {
		return
	}
	for _, slot := range ocrSubtitles(source) {
		if err := rt.resolveOCR(ctx, principal, source.ID, slot); err != nil {
			continue
		}
		stream := mediaStream{
			Codec: "srt", Type: mediaStreamSubtitle, Index: slot.Index, IsExternal: true, IsTextSubtitleStream: true,
			SupportsExternalStream: true, DeliveryMethod: subtitleDeliveryExternal,
			DeliveryURL: subtitleURL(itemID, info.ID, slot.Index, "srt"),
		}
		if slot.External != nil {
			stream.Language, stream.Title, stream.IsForced, stream.IsHearingImpaired = slot.External.Language, domain.OCRExternalTrackTitle(*slot.External), slot.External.Forced, slot.External.SDH
		} else {
			stream.Language, stream.Title, stream.IsForced = slot.Embedded.Language, domain.OCRTrackTitle(*slot.Embedded), slot.Embedded.Forced
		}
		info.MediaStreams = append(info.MediaStreams, stream)
	}
}
