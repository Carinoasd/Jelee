package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Device profile wire format (G24.2, G10.5). The DeviceProfile of a
// PlaybackInfo request is the client's capability declaration: the direct
// play, container, codec and subtitle profiles say what it can play as it
// is. media.GuardPlaybackInfo has already checked the whole request; this
// file only reads the declaration that passed it. Nothing here can make a
// conversion acceptable: a declaration only removes sources from the
// answer, and a declared transcoding profile, Encode or Hls subtitle method
// or MaxAudioChannels is read as nothing (the server never converts or
// downmixes). Reference: upstream MediaBrowser.Model/Dlna/DeviceProfile.cs,
// CodecProfile.cs, ContainerProfile.cs, ProfileCondition.cs,
// SubtitleProfile.cs and ConditionProcessor.cs.

// deviceProfile is the part of the upstream DeviceProfile that decides
// direct play. TranscodingProfiles, ResponseProfiles and the other members
// describe conversions or responses and are not read.
type deviceProfile struct {
	MaxStreamingBitrate *int64              `json:"MaxStreamingBitrate"`
	DirectPlayProfiles  []directPlayProfile `json:"DirectPlayProfiles"`
	ContainerProfiles   []containerProfile  `json:"ContainerProfiles"`
	CodecProfiles       []codecProfile      `json:"CodecProfiles"`
	SubtitleProfiles    []subtitleProfile   `json:"SubtitleProfiles"`
}

type directPlayProfile struct {
	Container  string      `json:"Container"`
	AudioCodec string      `json:"AudioCodec"`
	VideoCodec string      `json:"VideoCodec"`
	Type       profileType `json:"Type"`
}

type containerProfile struct {
	Type       profileType        `json:"Type"`
	Container  wireText           `json:"Container"`
	Conditions []profileCondition `json:"Conditions"`
}

type codecProfile struct {
	Type            wireText           `json:"Type"`
	Codec           wireText           `json:"Codec"`
	Container       wireText           `json:"Container"`
	Conditions      []profileCondition `json:"Conditions"`
	ApplyConditions []profileCondition `json:"ApplyConditions"`
}

type profileCondition struct {
	Condition wireText `json:"Condition"`
	Property  wireText `json:"Property"`
	Value     wireText `json:"Value"`
	// IsRequired defaults to true, as upstream constructs it.
	IsRequired *wireBool `json:"IsRequired"`
}

type subtitleProfile struct {
	Format    wireText `json:"Format"`
	Method    wireText `json:"Method"`
	Container wireText `json:"Container"`
	Language  wireText `json:"Language"`
}

var errProfileMember = errors.New("compat: unreadable profile member")

// wireText is a scalar profile member read as text. Clients send enum
// members by name or number and condition values as strings or numbers;
// null is empty. An object or array is malformed.
type wireText string

func (t *wireText) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	switch x := v.(type) {
	case nil:
		*t = ""
	case string:
		*t = wireText(x)
	case json.Number:
		*t = wireText(x.String())
	case bool:
		*t = wireText(strconv.FormatBool(x))
	default:
		return errProfileMember
	}
	return nil
}

// wireBool is a boolean member some clients send as a string.
type wireBool bool

func (b *wireBool) UnmarshalJSON(data []byte) error {
	var t wireText
	if err := t.UnmarshalJSON(data); err != nil {
		return err
	}
	v, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(string(t))))
	if err != nil {
		return errProfileMember
	}
	*b = wireBool(v)
	return nil
}

// Upstream enum numbers clients may send instead of names: CodecType,
// ProfileConditionType and SubtitleDeliveryMethod.
var (
	codecTypeNumbers      = []string{domain.CodecProfileVideo, domain.CodecProfileVideoAudio, domain.CodecProfileAudio}
	conditionTypeNumbers  = []string{domain.ConditionEquals, domain.ConditionNotEquals, domain.ConditionLessThanEqual, domain.ConditionGreaterThanEqual, domain.ConditionEqualsAny}
	subtitleMethodNumbers = []string{"Encode", domain.SubtitleMethodEmbed, domain.SubtitleMethodExternal, "Hls", "Drop"}
)

// enumName maps an enum number to its name and canonicalizes a name's case;
// anything else is kept as sent and matches nothing.
func enumName(value wireText, names []string) string {
	s := strings.TrimSpace(string(value))
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n < len(names) {
		return names[n]
	}
	for _, name := range names {
		if strings.EqualFold(name, s) {
			return name
		}
	}
	return s
}

// declaration converts the wire profile to the domain declaration.
func (p *deviceProfile) declaration() *domain.DeviceProfile {
	if p == nil {
		return nil
	}
	out := &domain.DeviceProfile{}
	for _, d := range p.DirectPlayProfiles {
		out.DirectPlay = append(out.DirectPlay, domain.DirectPlayProfile{
			Video:      strings.EqualFold(string(d.Type), string(dlnaProfileTypeVideo)),
			Containers: splitList(d.Container), VideoCodecs: splitList(d.VideoCodec), AudioCodecs: splitList(d.AudioCodec),
		})
	}
	for _, c := range p.ContainerProfiles {
		out.Containers = append(out.Containers, domain.ContainerProfile{
			Video:      strings.EqualFold(string(c.Type), string(dlnaProfileTypeVideo)),
			Containers: splitList(string(c.Container)), Conditions: conditions(c.Conditions),
		})
	}
	for _, c := range p.CodecProfiles {
		out.Codecs = append(out.Codecs, domain.CodecProfile{
			Type: enumName(c.Type, codecTypeNumbers), Codecs: splitList(string(c.Codec)), Containers: splitList(string(c.Container)),
			Conditions: conditions(c.Conditions), ApplyConditions: conditions(c.ApplyConditions),
		})
	}
	for _, s := range p.SubtitleProfiles {
		out.Subtitles = append(out.Subtitles, domain.SubtitleProfile{
			Format: strings.TrimSpace(string(s.Format)), Method: enumName(s.Method, subtitleMethodNumbers),
			Containers: splitList(string(s.Container)), Languages: splitList(string(s.Language)),
		})
	}
	return out
}

func conditions(in []profileCondition) []domain.ProfileCondition {
	out := make([]domain.ProfileCondition, 0, len(in))
	for _, c := range in {
		out = append(out, domain.ProfileCondition{
			Condition: enumName(c.Condition, conditionTypeNumbers), Property: strings.TrimSpace(string(c.Property)),
			Value: string(c.Value), IsRequired: c.IsRequired == nil || bool(*c.IsRequired),
		})
	}
	return out
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// unsupportedReasonNames maps exclusion codes to upstream TranscodeReason
// names (MediaBrowser.Model/Session/TranscodeReason.cs), listed in that
// enum's order, so a client can show them with the strings it already
// knows. The server only reports them: it never transcodes.
var unsupportedReasonNames = []struct{ code, name string }{
	{domain.PlaybackReasonContainer, "ContainerNotSupported"},
	{domain.PlaybackReasonVideoCodec, "VideoCodecNotSupported"},
	{domain.PlaybackReasonAudioCodec, "AudioCodecNotSupported"},
	{"IsSecondaryAudio", "SecondaryAudioNotSupported"},
	{"VideoProfile", "VideoProfileNotSupported"},
	{"VideoLevel", "VideoLevelNotSupported"},
	{"Width", "VideoResolutionNotSupported"},
	{"Height", "VideoResolutionNotSupported"},
	{"VideoBitDepth", "VideoBitDepthNotSupported"},
	{"VideoFramerate", "VideoFramerateNotSupported"},
	{"AudioChannels", "AudioChannelsNotSupported"},
	{"AudioProfile", "AudioProfileNotSupported"},
	{"AudioSampleRate", "AudioSampleRateNotSupported"},
	{"AudioBitDepth", "AudioBitDepthNotSupported"},
	{domain.PlaybackReasonBitrate, "ContainerBitrateExceedsLimit"},
	{"VideoBitrate", "VideoBitrateNotSupported"},
	{"AudioBitrate", "AudioBitrateNotSupported"},
	{"VideoRangeType", "VideoRangeTypeNotSupported"},
	{"NumVideoStreams", "StreamCountExceedsLimit"},
	{"NumAudioStreams", "StreamCountExceedsLimit"},
}

// unsupportedReasons is the JeleeUnsupportedReasons answer of a
// NoCompatibleStream: the upstream names of every capability reason that
// excluded a source, without duplicates, in upstream enum order. A source
// that was not requested or with direct play switched off says nothing
// about the client's capabilities and adds nothing.
func unsupportedReasons(excluded [][]string) []string {
	var keys []string
	for _, reasons := range excluded {
		for _, r := range reasons {
			if _, property, ok := strings.Cut(r, ":"); ok {
				r = property
			}
			keys = append(keys, r)
		}
	}
	var out []string
	for _, n := range unsupportedReasonNames {
		if slices.Contains(keys, n.code) && !slices.Contains(out, n.name) {
			out = append(out, n.name)
		}
	}
	return out
}

// logSubtitleProfiles reports, at info level, the subtitle streams of a
// listed source the client declared no way to read as they are
// (subtitle_format_unsupported). Only the source ID, the fixed code and
// stream indexes are logged. The listing itself is unchanged: the stream
// stays listed with its own format and delivery so the client can still
// decide, and nothing is converted for it.
func (rt *router) logSubtitleProfiles(ctx context.Context, req playbackRequest, source domain.PlaybackSource, info mediaSourceInfo) {
	logger := rt.opts.Library.Logger
	if logger == nil || req.profile == nil || len(req.profile.Subtitles) == 0 {
		return
	}
	var indexes []int
	for _, s := range info.MediaStreams {
		if s.Type != mediaStreamSubtitle {
			continue
		}
		candidate := domain.SubtitleCandidate{Format: s.Codec, Language: s.Language, External: s.IsExternal, Deliverable: s.DeliveryURL != ""}
		if domain.SubtitleMethod(req.profile.Subtitles, source.Container, candidate) == "" {
			indexes = append(indexes, s.Index)
		}
	}
	if len(indexes) > 0 {
		logger.InfoContext(ctx, "subtitle unsupported by client", "component", "compat", "sourceId", source.ID,
			"reason", domain.PlaybackReasonSubtitleFormat, "streams", indexes)
	}
}

// logExternalAudioProfiles evaluates each probed external audio file the
// way the declaration's direct play and codec profiles would judge it as
// the source's audio (domain.DeviceProfileReasons on the source with that
// file as its only audio stream) and records, at info level, the files the
// client declared it cannot decode (audio_codec_unsupported). Like a
// subtitle, the file stays listed with its DeliveryUrl: it never decides
// whether the source is offered, and nothing is converted for it. An
// unprobed file's codec is unknown and is not judged.
func (rt *router) logExternalAudioProfiles(ctx context.Context, req playbackRequest, source domain.PlaybackSource) {
	logger := rt.opts.Library.Logger
	if logger == nil || req.profile == nil || rt.opts.Library.Delivery == nil {
		return
	}
	var indexes []int
	for _, e := range domain.UpstreamExternalAudio(source) {
		t := e.Track
		if t.ProbeState != domain.SidecarProbeReady || t.Codec == "" {
			continue
		}
		single := source
		single.Parts, single.DefaultTracks = nil, nil
		audio := domain.PlaybackAudioTrack{Index: e.Index, Codec: t.Codec, Profile: t.Profile, Language: t.Language, ChannelLayout: t.ChannelLayout, Default: true}
		if t.Channels > 0 {
			channels := int64(t.Channels)
			audio.Channels = &channels
		}
		if t.SampleRate > 0 {
			rate := int64(t.SampleRate)
			audio.SampleRate = &rate
		}
		if t.BitRate > 0 {
			bitRate := t.BitRate
			audio.BitRate = &bitRate
		}
		single.Audio = []domain.PlaybackAudioTrack{audio}
		for _, reason := range domain.DeviceProfileReasons(req.profile, 0, single) {
			if reason == domain.PlaybackReasonAudioCodec || strings.HasPrefix(reason, domain.PlaybackReasonCodecCondition+":Audio") {
				indexes = append(indexes, e.Index)
				break
			}
		}
	}
	if len(indexes) > 0 {
		logger.InfoContext(ctx, "external audio unsupported by client", "component", "compat", "sourceId", source.ID,
			"reason", domain.PlaybackReasonAudioCodec, "streams", indexes)
	}
}

// logUnsupported records, at info level, why each source was left out of
// a NoCompatibleStream answer (G10.5): only source IDs and fixed codes.
func logUnsupported(ctx context.Context, logger *slog.Logger, sources []string, excluded [][]string) {
	if logger == nil {
		return
	}
	for i, id := range sources {
		logger.InfoContext(ctx, "direct play unsupported", "component", "compat", "sourceId", id, "reasons", excluded[i])
	}
}
