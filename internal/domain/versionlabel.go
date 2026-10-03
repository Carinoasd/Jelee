package domain

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// VersionLabelSource says which input decided a label. Probe observations read
// the bitstream; filename tokens are release-group or user text. When both
// speak to the same label and disagree, the probe wins and the disagreement is
// reported in VersionLabels.Conflicts instead of being silently dropped.
type VersionLabelSource string

const (
	VersionLabelFromProbe    VersionLabelSource = "probe"
	VersionLabelFromFilename VersionLabelSource = "filename"
)

type VersionResolution string

const (
	VersionResolutionSD    VersionResolution = "sd"
	VersionResolution240p  VersionResolution = "240p"
	VersionResolution360p  VersionResolution = "360p"
	VersionResolution480p  VersionResolution = "480p"
	VersionResolution576p  VersionResolution = "576p"
	VersionResolution720p  VersionResolution = "720p"
	VersionResolution1080p VersionResolution = "1080p"
	VersionResolution1440p VersionResolution = "1440p"
	VersionResolution2160p VersionResolution = "2160p"
	VersionResolution4320p VersionResolution = "4320p"
)

type VersionHDR string

const (
	VersionHDRDolbyVision VersionHDR = "dolby_vision"
	VersionHDR10Plus      VersionHDR = "hdr10plus"
	VersionHDR10          VersionHDR = "hdr10"
	VersionHDRHLG         VersionHDR = "hlg"
	// VersionHDRGeneric only comes from a bare "HDR" filename token, which names
	// no concrete format. A probe never produces it.
	VersionHDRGeneric VersionHDR = "hdr"
)

type VersionSource string

const (
	VersionSourceRemux  VersionSource = "remux"
	VersionSourceBluRay VersionSource = "bluray"
	VersionSourceWebDL  VersionSource = "web_dl"
	VersionSourceWebRip VersionSource = "webrip"
	VersionSourceHDTV   VersionSource = "hdtv"
	VersionSourceDVD    VersionSource = "dvd"
)

type VersionEdition string

const (
	VersionEditionDirectorsCut VersionEdition = "directors_cut"
	VersionEditionExtended     VersionEdition = "extended"
	VersionEditionUncut        VersionEdition = "uncut"
	VersionEditionUnrated      VersionEdition = "unrated"
	VersionEditionTheatrical   VersionEdition = "theatrical"
	VersionEditionFinalCut     VersionEdition = "final_cut"
	VersionEditionUltimate     VersionEdition = "ultimate"
	VersionEditionSpecial      VersionEdition = "special"
	VersionEditionRemastered   VersionEdition = "remastered"
	VersionEditionIMAX         VersionEdition = "imax"
	VersionEditionOpenMatte    VersionEdition = "open_matte"
	VersionEditionCriterion    VersionEdition = "criterion"
)

// VersionAudio describes the strongest audio track of a version. Codec is the
// normalized probe codec id ("truehd", "dts", ...); Format refines it with the
// profile ("DTS-HD MA", "TrueHD") for display.
type VersionAudio struct {
	Codec    string             `json:"codec,omitempty"`
	Format   string             `json:"format,omitempty"`
	Channels string             `json:"channels,omitempty"`
	Atmos    bool               `json:"atmos,omitempty"`
	DTSX     bool               `json:"dtsX,omitempty"`
	From     VersionLabelSource `json:"from,omitempty"`
	// ImmersiveFrom is set when Atmos or DTS:X came from a different input than
	// the codec, e.g. the probe saw TrueHD without a profile and the filename
	// said Atmos.
	ImmersiveFrom VersionLabelSource `json:"immersiveFrom,omitempty"`
}

type VersionLabelConflict struct {
	Field    string `json:"field"`
	Probe    string `json:"probe"`
	Filename string `json:"filename"`
}

// VersionLabels is the display/sort projection of one file version (G20.2).
// It is derived data: never persisted as truth, always recomputable from the
// normalized probe metadata plus the file name.
type VersionLabels struct {
	Resolution         VersionResolution      `json:"resolution,omitempty"`
	ResolutionFrom     VersionLabelSource     `json:"resolutionFrom,omitempty"`
	HDR                []VersionHDR           `json:"hdr,omitempty"`
	HDRFrom            VersionLabelSource     `json:"hdrFrom,omitempty"`
	DolbyVisionProfile *int64                 `json:"dolbyVisionProfile,omitempty"`
	VideoCodec         string                 `json:"videoCodec,omitempty"`
	VideoCodecFrom     VersionLabelSource     `json:"videoCodecFrom,omitempty"`
	Audio              VersionAudio           `json:"audio"`
	Source             VersionSource          `json:"source,omitempty"`
	Editions           []VersionEdition       `json:"editions,omitempty"`
	CustomEdition      string                 `json:"customEdition,omitempty"`
	QualityScore       int64                  `json:"qualityScore"`
	DisplayName        string                 `json:"displayName"`
	Conflicts          []VersionLabelConflict `json:"conflicts,omitempty"`
}

// VersionLabelsFromProbe derives version labels from normalized probe metadata
// and the file's name. Only the last path element of filename is inspected so
// directory names (often the title or a collection) cannot leak edition tokens.
// A zero MediaMetadata is valid and yields filename-only labels.
//
// Precedence: technical labels (resolution, HDR, codecs, Atmos) come from the
// probe whenever the probe observed the relevant field; filename tokens only
// fill gaps. Release labels (source, edition) exist only in the filename.
func VersionLabelsFromProbe(meta MediaMetadata, filename string) VersionLabels {
	name := parseVersionFilename(filename)
	var l VersionLabels
	video := primaryVideoStream(meta)

	var probeRes VersionResolution
	if video != nil && video.Video.Width != nil && video.Video.Height != nil {
		probeRes = resolutionTier(*video.Video.Width, *video.Video.Height)
	}
	l.Resolution, l.ResolutionFrom = pickLabel(&l, "resolution", probeRes, name.resolution)

	if video != nil {
		if hdr, known, dvProfile := probeHDR(*video.Video); known {
			l.HDR, l.HDRFrom, l.DolbyVisionProfile = hdr, VersionLabelFromProbe, dvProfile
			if c, ok := hdrConflict(hdr, name.hdr); ok {
				l.Conflicts = append(l.Conflicts, c)
			}
		}
	}
	if l.HDRFrom == "" && len(name.hdr) > 0 {
		l.HDR, l.HDRFrom = name.hdr, VersionLabelFromFilename
	}

	var probeCodec string
	if video != nil && video.Codec != nil {
		probeCodec = *video.Codec
	}
	l.VideoCodec, l.VideoCodecFrom = pickLabel(&l, "videoCodec", probeCodec, name.videoCodec)

	l.Audio = deriveVersionAudio(meta, name, &l.Conflicts)
	l.Source, l.Editions, l.CustomEdition = name.source, name.editions, name.customEdition
	l.QualityScore = versionQualityScore(l)
	l.DisplayName = versionDisplayName(l)
	return l
}

func pickLabel[T ~string](l *VersionLabels, field string, probe, filename T) (T, VersionLabelSource) {
	switch {
	case probe != "":
		if filename != "" && filename != probe {
			l.Conflicts = append(l.Conflicts, VersionLabelConflict{Field: field, Probe: string(probe), Filename: string(filename)})
		}
		return probe, VersionLabelFromProbe
	case filename != "":
		return filename, VersionLabelFromFilename
	}
	return "", ""
}

// Still-image codecs carried as video streams are cover art, not the feature.
var versionImageCodecs = metadataWords("mjpeg|png|apng|gif|webp|bmp|tiff")

// primaryVideoStream prefers a default-flagged stream, then the largest frame,
// then the lowest index so the choice is deterministic.
func primaryVideoStream(meta MediaMetadata) *MediaStream {
	var best *MediaStream
	area := func(s *MediaStream) int64 {
		if s.Video.Width == nil || s.Video.Height == nil {
			return 0
		}
		return *s.Video.Width * *s.Video.Height
	}
	for i := range meta.Streams {
		s := &meta.Streams[i]
		if s.Kind != "video" || s.Video == nil || s.Codec != nil && versionImageCodecs[*s.Codec] {
			continue
		}
		if best == nil {
			best = s
			continue
		}
		sd, bd := s.Default != nil && *s.Default, best.Default != nil && *best.Default
		switch {
		case sd != bd:
			if sd {
				best = s
			}
		case area(s) != area(best):
			if area(s) > area(best) {
				best = s
			}
		case s.Index < best.Index:
			best = s
		}
	}
	return best
}

type resolutionTierSpec struct {
	tier          VersionResolution
	width, height int64
}

var resolutionTiers = []resolutionTierSpec{
	{VersionResolution4320p, 7680, 4320}, {VersionResolution2160p, 3840, 2160}, {VersionResolution1440p, 2560, 1440},
	{VersionResolution1080p, 1920, 1080}, {VersionResolution720p, 1280, 720}, {VersionResolution576p, 1024, 576},
	{VersionResolution480p, 854, 480}, {VersionResolution360p, 640, 360}, {VersionResolution240p, 426, 240},
}

// resolutionTier classifies by frame size, not by name. A frame reaches a tier
// when either its long edge reaches ~85% of the tier's 16:9 width (scope crops
// such as 1920x800 or 3840x1600 keep the full width) or its short edge reaches
// ~85% of the tier height (pillarboxed or 4:3 frames such as 1440x1080 keep the
// full height). Using long/short edges makes portrait video classify like its
// landscape twin. The 85% margin absorbs encoder crops like 1916x1036 without
// promoting 1600x900 to 1080p. Pixel aspect ratio is not in the probe whitelist,
// so anamorphic SD is classified by its stored height, which is the usual label.
func resolutionTier(width, height int64) VersionResolution {
	if width <= 0 || height <= 0 {
		return ""
	}
	long, short := width, height
	if short > long {
		long, short = short, long
	}
	for _, t := range resolutionTiers {
		if long*100 >= t.width*85 || short*100 >= t.height*85 {
			return t.tier
		}
	}
	return VersionResolutionSD
}

// probeHDR reports the HDR formats a decoder can use, strongest first, and
// whether the probe said anything about dynamic range at all. A Dolby Vision
// record contributes the DV layer; the base layer's fallback is then decided by
// the stream's own transfer, except for profile 5 / compatibility id 0, whose
// IPT base layer is not HDR10-playable even when tagged PQ.
func probeHDR(v MediaVideo) ([]VersionHDR, bool, *int64) {
	transfer := ""
	if v.ColorTransfer != nil {
		transfer = *v.ColorTransfer
	}
	plus := v.HDR10Plus != nil && *v.HDR10Plus
	staticMeta := v.MasteringDisplay != nil || v.ContentLight != nil
	known := transfer != "" || v.DolbyVision != nil || plus || staticMeta
	if !known {
		return nil, false, nil
	}
	var out []VersionHDR
	var profile *int64
	noFallback := false
	if dv := v.DolbyVision; dv != nil {
		out = append(out, VersionHDRDolbyVision)
		if dv.Profile != nil {
			p := *dv.Profile
			profile = &p
			noFallback = p == 5
		}
		if dv.CompatibilityID != nil {
			switch *dv.CompatibilityID {
			case 0:
				noFallback = true
			case 4:
				// Profile 8.4 carries an HLG base layer.
				if transfer == "" {
					transfer = "arib-std-b67"
				}
			}
		}
	}
	// PQ is inferred only when the probe omitted the transfer but still found
	// PQ-only metadata; an explicit non-PQ transfer always wins.
	pq := transfer == "smpte2084" || transfer == "" && (plus || staticMeta)
	if pq && !noFallback {
		if plus {
			out = append(out, VersionHDR10Plus)
		}
		out = append(out, VersionHDR10)
	}
	if transfer == "arib-std-b67" && !noFallback {
		out = append(out, VersionHDRHLG)
	}
	return out, true, profile
}

// hdrConflict reports filename HDR claims the probe does not support. A bare
// "HDR" token conflicts only with an SDR probe.
func hdrConflict(probe, filename []VersionHDR) (VersionLabelConflict, bool) {
	have := make(map[VersionHDR]bool, len(probe))
	for _, h := range probe {
		have[h] = true
	}
	for _, h := range filename {
		if h == VersionHDRGeneric && len(probe) > 0 || have[h] {
			continue
		}
		return VersionLabelConflict{Field: "hdr", Probe: joinHDR(probe), Filename: joinHDR(filename)}, true
	}
	return VersionLabelConflict{}, false
}

func joinHDR(v []VersionHDR) string {
	if len(v) == 0 {
		return "sdr"
	}
	parts := make([]string, len(v))
	for i, h := range v {
		parts[i] = string(h)
	}
	return strings.Join(parts, "+")
}

func deriveVersionAudio(meta MediaMetadata, name versionFilename, conflicts *[]VersionLabelConflict) VersionAudio {
	var best VersionAudio
	bestRank, bestDefault, found := -1, false, false
	codecs := map[string]bool{}
	for _, s := range meta.Streams {
		if s.Kind != "audio" || s.Audio == nil || s.Codec == nil {
			continue
		}
		a := probeAudio(s)
		codecs[a.Codec] = true
		// Strongest track wins; the default flag, then stream order, break ties.
		def := s.Default != nil && *s.Default
		if r := audioRank(a); r > bestRank || r == bestRank && def && !bestDefault {
			best, bestRank, bestDefault, found = a, r, def, true
		}
	}
	if !found {
		if name.audio.Codec == "" && !name.audio.Atmos && !name.audio.DTSX {
			return VersionAudio{}
		}
		a := name.audio
		a.From = VersionLabelFromFilename
		return a
	}
	if name.audio.Codec != "" && !codecs[name.audio.Codec] {
		*conflicts = append(*conflicts, VersionLabelConflict{Field: "audioCodec", Probe: best.Codec, Filename: name.audio.Codec})
	}
	if best.Channels == "" && name.audio.Channels != "" && name.audio.Codec == best.Codec {
		best.Channels = name.audio.Channels
	}
	// The probe only reports Atmos/DTS:X through the codec profile. When the
	// profile is absent (older ffprobe, or a profile outside the whitelist such
	// as "DTS-HD MA + DTS:X"), a filename claim compatible with the codec is
	// accepted and marked as filename-sourced. A known, non-immersive profile
	// means the probe looked and found none: that is a conflict, probe wins.
	if name.audio.Atmos && !best.Atmos {
		switch {
		case (best.Codec == "truehd" || best.Codec == "eac3") && best.Format == "":
			best.Atmos, best.ImmersiveFrom = true, VersionLabelFromFilename
		default:
			*conflicts = append(*conflicts, VersionLabelConflict{Field: "atmos", Probe: audioConflictText(best), Filename: "atmos"})
		}
	}
	if name.audio.DTSX && !best.DTSX {
		switch {
		case best.Codec == "dts" && (best.Format == "" || best.Format == "DTS-HD MA"):
			best.DTSX, best.ImmersiveFrom = true, VersionLabelFromFilename
		default:
			*conflicts = append(*conflicts, VersionLabelConflict{Field: "dtsX", Probe: audioConflictText(best), Filename: "dts:x"})
		}
	}
	return best
}

func audioConflictText(a VersionAudio) string {
	if a.Format != "" && a.Codec != "dts" {
		return a.Codec + " (" + a.Format + ")"
	}
	return strings.TrimSpace(a.Codec + " " + a.Format)
}

func probeAudio(s MediaStream) VersionAudio {
	a := VersionAudio{Codec: *s.Codec, From: VersionLabelFromProbe, Channels: audioChannels(s.Audio)}
	profile := ""
	if s.Profile != nil {
		profile = *s.Profile
	}
	a.Atmos = s.Audio.Atmos != nil && *s.Audio.Atmos
	switch a.Codec {
	case "truehd":
		if profile != "" {
			a.Format = "TrueHD"
		}
		a.Atmos = a.Atmos || profile == "Dolby TrueHD + Dolby Atmos"
	case "eac3":
		if profile != "" {
			a.Format = "DD+"
		}
		a.Atmos = a.Atmos || profile == "Dolby Digital Plus + Dolby Atmos"
	case "dts":
		// "+ DTS:X" profiles are outside today's probe whitelist; they are
		// handled so a whitelist extension needs no change here.
		switch {
		case strings.HasSuffix(profile, "DTS:X"):
			a.Format, a.DTSX = "DTS-HD MA", true
		case profile != "":
			a.Format = profile
		}
	}
	return a
}

func audioChannels(a *MediaAudio) string {
	if a.ChannelLayout != nil {
		layout := *a.ChannelLayout
		if i := strings.IndexByte(layout, '('); i >= 0 {
			layout = layout[:i]
		}
		switch layout {
		case "mono":
			return "1.0"
		case "stereo", "downmix":
			return "2.0"
		case "quad":
			return "4.0"
		case "hexagonal":
			return "6.0"
		case "octagonal":
			return "8.0"
		}
		if layout != "" && layout[0] >= '0' && layout[0] <= '9' {
			return layout
		}
	}
	if a.Channels != nil {
		switch n := *a.Channels; n {
		case 1:
			return "1.0"
		case 2:
			return "2.0"
		case 6:
			return "5.1"
		case 8:
			return "7.1"
		default:
			return strconv.FormatInt(n, 10) + "ch"
		}
	}
	return ""
}

var lossless = metadataWords("flac|alac|pcm_s16le|pcm_s16be|pcm_s24le|pcm_s24be|pcm_s32le|pcm_s32be|pcm_f32le|pcm_f64le|pcm_u8|pcm")

func audioRank(a VersionAudio) int {
	switch {
	case a.Codec == "truehd" && a.Atmos:
		return 9
	case a.DTSX:
		return 8
	case a.Codec == "truehd" || a.Codec == "dts" && a.Format == "DTS-HD MA":
		return 7
	case a.Codec == "eac3" && a.Atmos:
		return 6
	case lossless[a.Codec]:
		return 6
	case a.Codec == "dts" && a.Format == "DTS-HD HRA":
		return 5
	case a.Codec == "dts" || a.Codec == "eac3":
		return 4
	case a.Codec == "ac3":
		return 3
	case a.Codec == "aac" || a.Codec == "opus" || a.Codec == "vorbis":
		return 2
	case a.Codec != "":
		return 1
	}
	return 0
}

var resolutionRank = map[VersionResolution]int64{
	VersionResolutionSD: 1, VersionResolution240p: 2, VersionResolution360p: 3, VersionResolution480p: 4, VersionResolution576p: 5,
	VersionResolution720p: 6, VersionResolution1080p: 7, VersionResolution1440p: 8, VersionResolution2160p: 9, VersionResolution4320p: 10,
}

var sourceRank = map[VersionSource]int64{
	VersionSourceRemux: 6, VersionSourceBluRay: 5, VersionSourceWebDL: 4, VersionSourceWebRip: 3, VersionSourceHDTV: 2, VersionSourceDVD: 1,
}

var hdrRank = map[VersionHDR]int64{VersionHDRDolbyVision: 5, VersionHDR10Plus: 4, VersionHDR10: 3, VersionHDRHLG: 2, VersionHDRGeneric: 1}

var videoCodecRank = map[string]int64{"av1": 5, "hevc": 4, "vp9": 3, "h264": 2, "vc1": 1, "mpeg2video": 1, "mpeg4": 1}

// versionQualityScore orders versions by resolution, then HDR capability, then
// release source, then audio, then video codec. Each component occupies its own
// decimal digit pair so a lower-priority component can never outweigh a higher
// one. Unknown components score 0. Bitrate is deliberately excluded so the score
// depends only on labels the user can see; callers break ties by stable ids.
func versionQualityScore(l VersionLabels) int64 {
	hdr := int64(0)
	for _, h := range l.HDR {
		hdr = max(hdr, hdrRank[h])
	}
	return resolutionRank[l.Resolution]*100000000 + hdr*1000000 + sourceRank[l.Source]*10000 + int64(audioRank(l.Audio))*100 + videoCodecRank[l.VideoCodec]
}

var videoCodecNames = map[string]string{
	"h264": "H.264", "hevc": "HEVC", "av1": "AV1", "vp9": "VP9", "vp8": "VP8", "mpeg2video": "MPEG-2", "mpeg1video": "MPEG-1",
	"mpeg4": "MPEG-4", "vc1": "VC-1", "wmv3": "WMV", "prores": "ProRes", "dnxhd": "DNxHD",
}
var audioCodecNames = map[string]string{
	"truehd": "TrueHD", "eac3": "DD+", "ac3": "DD", "dts": "DTS", "aac": "AAC", "flac": "FLAC", "alac": "ALAC", "opus": "Opus",
	"vorbis": "Vorbis", "mp3": "MP3", "mp2": "MP2", "pcm": "PCM",
}
var hdrNames = map[VersionHDR]string{VersionHDRDolbyVision: "DV", VersionHDR10Plus: "HDR10+", VersionHDR10: "HDR10", VersionHDRHLG: "HLG", VersionHDRGeneric: "HDR"}
var sourceNames = map[VersionSource]string{VersionSourceRemux: "REMUX", VersionSourceBluRay: "BluRay", VersionSourceWebDL: "WEB-DL", VersionSourceWebRip: "WEBRip", VersionSourceHDTV: "HDTV", VersionSourceDVD: "DVD"}
var editionNames = map[VersionEdition]string{
	VersionEditionDirectorsCut: "Director's Cut", VersionEditionExtended: "Extended", VersionEditionUncut: "Uncut", VersionEditionUnrated: "Unrated",
	VersionEditionTheatrical: "Theatrical", VersionEditionFinalCut: "Final Cut", VersionEditionUltimate: "Ultimate", VersionEditionSpecial: "Special Edition",
	VersionEditionRemastered: "Remastered", VersionEditionIMAX: "IMAX", VersionEditionOpenMatte: "Open Matte", VersionEditionCriterion: "Criterion",
}

// versionDisplayName builds a locale-neutral technical name such as
// "2160p · DV HDR10 · HEVC · TrueHD Atmos 7.1 · REMUX · Director's Cut".
// Clients may localize editions from the Editions codes instead.
func versionDisplayName(l VersionLabels) string {
	var parts []string
	switch l.Resolution {
	case "":
	case VersionResolutionSD:
		parts = append(parts, "SD")
	default:
		parts = append(parts, string(l.Resolution))
	}
	// Show DV plus the strongest base-layer format; HDR10 is implied by HDR10+.
	var hdr []string
	for _, h := range l.HDR {
		if h == VersionHDR10 && len(hdr) > 0 && hdr[len(hdr)-1] == "HDR10+" {
			continue
		}
		hdr = append(hdr, hdrNames[h])
	}
	if len(hdr) > 0 {
		parts = append(parts, strings.Join(hdr, " "))
	}
	if l.VideoCodec != "" {
		parts = append(parts, displayCodec(videoCodecNames, l.VideoCodec))
	}
	if a := l.Audio; a.Codec != "" || a.Atmos || a.DTSX {
		var audio []string
		switch {
		case a.DTSX:
			audio = append(audio, "DTS:X")
		case a.Format != "" && a.Codec == "dts":
			audio = append(audio, a.Format)
		case a.Codec != "":
			audio = append(audio, displayCodec(audioCodecNames, a.Codec))
		}
		if a.Atmos {
			audio = append(audio, "Atmos")
		}
		if a.Channels != "" {
			audio = append(audio, a.Channels)
		}
		parts = append(parts, strings.Join(audio, " "))
	}
	if l.Source != "" {
		parts = append(parts, sourceNames[l.Source])
	}
	for _, e := range l.Editions {
		parts = append(parts, editionNames[e])
	}
	if l.CustomEdition != "" {
		parts = append(parts, l.CustomEdition)
	}
	return strings.Join(parts, " · ")
}

func displayCodec(names map[string]string, codec string) string {
	if n, ok := names[codec]; ok {
		return n
	}
	if strings.HasPrefix(codec, "pcm_") {
		return "PCM"
	}
	return strings.ToUpper(codec)
}

type versionFilename struct {
	resolution    VersionResolution
	hdr           []VersionHDR
	videoCodec    string
	audio         VersionAudio
	source        VersionSource
	editions      []VersionEdition
	customEdition string
}

type versionToken[T any] struct {
	value    T
	patterns []string
}

var (
	versionResolutionToken = regexp.MustCompile(`(?:^|[^0-9a-z])(?:[a-z]{0,3})(4320|2160|1440|1080|720|576|480|360|240)[pi](?:[^0-9a-z]|$)`)
	versionSizeToken       = regexp.MustCompile(`(?:^|[^0-9])([1-9][0-9]{2,3})[x×]([1-9][0-9]{2,3})(?:[^0-9]|$)`)
	versionYearToken       = regexp.MustCompile(` (?:19|20)[0-9]{2} `)
	versionEditionBrace    = regexp.MustCompile(`(?i)\{edition-([^{}]{1,64})\}`)
	versionAudioGlued      = regexp.MustCompile(`(^|[^a-z])(ddp|dd|aac|truehd|dts|dtshd|ma|flac|opus|lpcm|pcm|atmos)([1-9]\.[01])`)
	versionAudioChannels   = regexp.MustCompile(` (?:truehd|ddp|eac3|ac3|dd|dtshd ?ma|dtshd|dtsx|dts|aac|flac|lpcm|pcm|opus|atmos) ?([1-9]) ([01]) `)
)

// Tokens are matched against a separator-normalized, space-padded lowercase
// name, so "Directors.Cut", "directors_cut" and "Director's Cut" all become
// " directors cut ". CJK tokens are matched against the same text with spaces
// removed so "ディレクターズ・カット" and "ディレクターズカット" are equal.
var versionHDRTokens = []versionToken[VersionHDR]{
	{VersionHDRDolbyVision, []string{" dv ", " dovi ", " dolby vision ", " dolbyvision ", "杜比视界", "杜比視界", "ドルビービジョン"}},
	{VersionHDR10Plus, []string{" hdr10plus ", " hdr10p "}},
	{VersionHDR10, []string{" hdr10 "}},
	{VersionHDRHLG, []string{" hlg "}},
	{VersionHDRGeneric, []string{" hdr "}},
}
var versionVideoCodecTokens = []versionToken[string]{
	{"hevc", []string{" x265 ", " h265 ", " h 265 ", " hevc "}},
	{"h264", []string{" x264 ", " h264 ", " h 264 ", " avc "}},
	{"av1", []string{" av1 "}},
	{"vp9", []string{" vp9 "}},
	{"vc1", []string{" vc1 ", " vc 1 "}},
	{"mpeg2video", []string{" mpeg2 ", " mpeg 2 "}},
	{"mpeg4", []string{" xvid ", " divx "}},
}
var versionAudioCodecTokens = []versionToken[string]{
	{"truehd", []string{" truehd "}},
	{"dts", []string{" dtsx ", " dtshd ", " dtshdma ", " dts "}},
	{"eac3", []string{" ddp ", " eac3 "}},
	{"ac3", []string{" ac3 ", " dd "}},
	{"flac", []string{" flac "}},
	{"pcm", []string{" lpcm ", " pcm "}},
	{"aac", []string{" aac "}},
	{"opus", []string{" opus "}},
}
var versionSourceTokens = []versionToken[VersionSource]{
	{VersionSourceRemux, []string{" remux ", " bdremux ", " bdmux "}},
	{VersionSourceBluRay, []string{" bluray ", " blu ray ", " bdrip ", " brrip ", " bd ", "蓝光", "藍光", "ブルーレイ"}},
	{VersionSourceWebRip, []string{" webrip ", " web rip "}},
	{VersionSourceWebDL, []string{" webdl ", " web dl ", " web "}},
	{VersionSourceHDTV, []string{" hdtv ", " hdtvrip "}},
	{VersionSourceDVD, []string{" dvdrip ", " dvd ", " dvd9 ", " dvd5 "}},
}
var versionEditionTokens = []versionToken[VersionEdition]{
	{VersionEditionDirectorsCut, []string{" directors cut ", " director cut ", " directors edition ", " directors version ", "导演剪辑", "導演剪輯", "导演版", "導演版", "ディレクターズカット"}},
	{VersionEditionExtended, []string{" extended ", " extended cut ", " extended edition ", "加长版", "加長版", "加长剪辑", "加長剪輯", "完全版", "エクステンデッド"}},
	{VersionEditionUncut, []string{" uncut ", "未删减", "未刪減", "无删减", "無刪減", "ノーカット"}},
	{VersionEditionUnrated, []string{" unrated ", "未分级", "未分級"}},
	{VersionEditionTheatrical, []string{" theatrical ", " theatrical cut ", "院线版", "院線版", "劇場公開版"}},
	{VersionEditionFinalCut, []string{" final cut ", "最终剪辑", "最終剪輯", "ファイナルカット"}},
	{VersionEditionUltimate, []string{" ultimate cut ", " ultimate edition ", "终极版", "終極版", "アルティメット"}},
	{VersionEditionSpecial, []string{" special edition ", "特别版", "特別版", "スペシャルエディション"}},
	{VersionEditionRemastered, []string{" remastered ", " remaster ", "重制版", "重製版", "修复版", "修復版", "リマスター"}},
	{VersionEditionIMAX, []string{" imax ", " imax edition "}},
	{VersionEditionOpenMatte, []string{" open matte ", " openmatte "}},
	{VersionEditionCriterion, []string{" criterion ", "标准收藏", "標準收藏"}},
}

// Replacements applied before separators are folded, for tokens whose
// punctuation carries meaning.
var versionFilenameReplacer = strings.NewReplacer(
	"hdr10+", " hdr10plus ", "dd+", " ddp ", "dts:x", " dtsx ", "dts-x", " dtsx ", "dts-hd", " dtshd ", "e-ac-3", " eac3 ",
	"web-dl", " webdl ", "blu-ray", " bluray ", "'", "", "’", "",
)

func parseVersionFilename(filename string) versionFilename {
	var out versionFilename
	base := filename
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 && len(base)-i <= 6 && isASCIIAlnum(base[i+1:]) {
		base = base[:i]
	}
	if !utf8.ValidString(base) || base == "" {
		return out
	}
	// Library-style "{edition-...}" names the edition explicitly; an unknown
	// value is kept verbatim (whitespace-folded) as a custom edition.
	if m := versionEditionBrace.FindStringSubmatch(base); m != nil {
		base = strings.Replace(base, m[0], " ", 1)
		edition := normalizeVersionName(versionFilenameReplacer.Replace(strings.ToLower(m[1])))
		if e, ok := matchVersionToken(versionEditionTokens, edition, strings.ReplaceAll(edition, " ", "")); ok {
			out.editions = append(out.editions, e)
		} else if text := strings.Join(strings.Fields(m[1]), " "); text != "" {
			out.customEdition = text
		}
	}
	lower := strings.ToLower(base)
	norm := normalizeVersionName(versionAudioGlued.ReplaceAllString(versionFilenameReplacer.Replace(lower), "$1$2 $3"))
	compact := strings.ReplaceAll(norm, " ", "")

	if m := versionResolutionToken.FindAllStringSubmatch(lower, -1); m != nil {
		n, _ := strconv.Atoi(m[len(m)-1][1])
		out.resolution = filenameResolution(n)
	} else if m := versionSizeToken.FindStringSubmatch(lower); m != nil {
		w, _ := strconv.ParseInt(m[1], 10, 64)
		h, _ := strconv.ParseInt(m[2], 10, 64)
		out.resolution = resolutionTier(w, h)
	} else {
		switch {
		case strings.Contains(norm, " 8k "):
			out.resolution = VersionResolution4320p
		case strings.Contains(norm, " 4k ") || strings.Contains(norm, " uhd "):
			out.resolution = VersionResolution2160p
		case strings.Contains(norm, " fhd "):
			out.resolution = VersionResolution1080p
		}
	}

	seen := map[VersionHDR]bool{}
	for _, t := range versionHDRTokens {
		if matchAny(t.patterns, norm, compact) && !seen[t.value] {
			seen[t.value] = true
			out.hdr = append(out.hdr, t.value)
		}
	}
	if len(out.hdr) > 1 && seen[VersionHDRGeneric] {
		out.hdr = out.hdr[:len(out.hdr)-1] // generic is last; a concrete format names it better
	}
	if seen[VersionHDR10Plus] && !seen[VersionHDR10] {
		out.hdr = insertAfter(out.hdr, VersionHDR10Plus, VersionHDR10)
	}

	out.videoCodec, _ = matchVersionToken(versionVideoCodecTokens, norm, compact)
	out.audio.Codec, _ = matchVersionToken(versionAudioCodecTokens, norm, compact)
	out.audio.Atmos = strings.Contains(norm, " atmos ") || strings.Contains(compact, "全景声") || strings.Contains(compact, "全景聲")
	out.audio.DTSX = strings.Contains(norm, " dtsx ")
	if strings.Contains(norm, " dtshd ma ") || strings.Contains(norm, " dtshdma ") {
		out.audio.Format = "DTS-HD MA"
	}
	if m := versionAudioChannels.FindStringSubmatch(norm); m != nil {
		out.audio.Channels = m[1] + "." + m[2]
	}

	// Release words also occur in titles ("The Final Cut", "Extended Family").
	// When a year is present, scene and library naming put release words after
	// it, so only the tail is searched for Latin tokens. CJK tokens are distinct
	// enough to search everywhere.
	tail := norm
	if loc := versionYearToken.FindStringIndex(norm); loc != nil {
		tail = norm[loc[1]-1:]
	}
	out.source, _ = matchVersionToken(versionSourceTokens, tail, compact)
	for _, t := range versionEditionTokens {
		if matchAny(t.patterns, tail, compact) && !containsEdition(out.editions, t.value) {
			out.editions = append(out.editions, t.value)
		}
	}
	sort.Slice(out.editions, func(i, j int) bool { return editionOrder(out.editions[i]) < editionOrder(out.editions[j]) })
	return out
}

func filenameResolution(n int) VersionResolution {
	for _, t := range resolutionTiers {
		if int64(n) == t.height {
			return t.tier
		}
	}
	return ""
}

func normalizeVersionName(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(' ')
	space, prevCJK := true, false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			// CJK text is written without separators, so "原盘REMUX" must
			// still expose " remux " as a Latin token.
			cjk := unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
			if !space && cjk != prevCJK {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space, prevCJK = false, cjk
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

func matchAny(patterns []string, latin, compact string) bool {
	for _, p := range patterns {
		if p[0] == ' ' {
			if strings.Contains(latin, p) {
				return true
			}
		} else if strings.Contains(compact, p) {
			return true
		}
	}
	return false
}

func matchVersionToken[T any](tokens []versionToken[T], latin, compact string) (T, bool) {
	for _, t := range tokens {
		if matchAny(t.patterns, latin, compact) {
			return t.value, true
		}
	}
	var zero T
	return zero, false
}

func containsEdition(v []VersionEdition, e VersionEdition) bool {
	for _, x := range v {
		if x == e {
			return true
		}
	}
	return false
}

func editionOrder(e VersionEdition) int {
	for i, t := range versionEditionTokens {
		if t.value == e {
			return i
		}
	}
	return len(versionEditionTokens)
}

func insertAfter(v []VersionHDR, after, value VersionHDR) []VersionHDR {
	for i, h := range v {
		if h == after {
			return append(v[:i+1], append([]VersionHDR{value}, v[i+1:]...)...)
		}
	}
	return v
}

func isASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}
