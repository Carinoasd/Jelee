package domain

import (
	"slices"
	"strconv"
	"strings"
)

// Device profile evaluation (G10.5, G24.2). An upstream client describes
// what it can play in a device profile; this server reads the profile as a
// pure capability declaration. It decides whether an original resource can
// be offered as it is and, when not, why. Nothing here ever selects,
// requests or describes a conversion: a profile member that only concerns
// conversions (transcoding profiles, Encode and Hls subtitle methods) is
// read as "this cannot be played as it is".

// Device profile exclusion codes. The direct play codes above
// (PlaybackReasonContainer, ...) are shared with the server's own API; the
// condition codes carry the upstream property name after the colon.
const (
	PlaybackReasonContainerCondition = "container_condition_unmet"
	PlaybackReasonCodecCondition     = "codec_condition_unmet"
)

// DeviceProfile is the direct play part of an upstream device profile.
// Lists hold the client's raw comma-separated tokens split into items.
type DeviceProfile struct {
	DirectPlay []DirectPlayProfile
	Containers []ContainerProfile
	Codecs     []CodecProfile
	Subtitles  []SubtitleProfile
}

// DirectPlayProfile accepts the listed containers and codecs as they are.
// Video is false for any profile type other than Video. Empty lists accept
// everything.
type DirectPlayProfile struct {
	Video       bool
	Containers  []string
	VideoCodecs []string
	AudioCodecs []string
}

// Profile types of a codec profile (upstream CodecType).
const (
	CodecProfileVideo      = "Video"
	CodecProfileVideoAudio = "VideoAudio"
	CodecProfileAudio      = "Audio"
)

// CodecProfile restricts the streams of one codec type: when Codecs and
// Containers accept the stream (empty lists accept every value) and every
// ApplyConditions member holds, every Conditions member must hold.
type CodecProfile struct {
	Type            string
	Codecs          []string
	Containers      []string
	Conditions      []ProfileCondition
	ApplyConditions []ProfileCondition
}

// ContainerProfile restricts video sources in the listed containers.
type ContainerProfile struct {
	Video      bool
	Containers []string
	Conditions []ProfileCondition
}

// ProfileCondition is one upstream ProfileCondition. Condition and Property
// are the upstream enum names; IsRequired decides an unknown value.
type ProfileCondition struct {
	Condition  string
	Property   string
	Value      string
	IsRequired bool
}

// Profile condition types (upstream ProfileConditionType).
const (
	ConditionEquals           = "Equals"
	ConditionNotEquals        = "NotEquals"
	ConditionLessThanEqual    = "LessThanEqual"
	ConditionGreaterThanEqual = "GreaterThanEqual"
	ConditionEqualsAny        = "EqualsAny"
)

// SubtitleProfile says the client reads Format delivered with Method.
type SubtitleProfile struct {
	Format     string
	Method     string
	Containers []string
	Languages  []string
}

// Subtitle delivery methods. Only Embed and External deliver a subtitle as
// it is; the upstream Encode and Hls methods are conversions and Drop means
// the client will not show it, so none of them is ever chosen.
const (
	SubtitleMethodEmbed    = "Embed"
	SubtitleMethodExternal = "External"
)

// conditionProperties are the upstream ProfileConditionValue names this
// server evaluates, in the order reasons are reported. Each has a value
// from the probe; one the probe did not find is unknown, which fails only
// a required condition.
var conditionProperties = []string{
	"VideoProfile", "VideoLevel", "VideoBitDepth", "VideoRangeType", "Width", "Height", "VideoFramerate", "VideoBitrate",
	"NumVideoStreams", "NumAudioStreams",
	"AudioChannels", "AudioSampleRate", "AudioBitDepth", "AudioProfile", "AudioBitrate", "IsSecondaryAudio",
}

// UnmeasuredConditionProperties are upstream properties the probe does not
// record (reference frames, interlacing, anamorphic pixels, codec tag, AVC
// framing, 64 bit offsets, packet length, timestamps). A condition on them
// is not evaluated at all, whatever IsRequired says: the server cannot know
// the value for any source, so holding it against every source would hide
// files the client can play. Every other unknown property is ignored too.
var UnmeasuredConditionProperties = []string{
	"RefFrames", "IsInterlaced", "IsAnamorphic", "VideoCodecTag", "IsAvc", "Has64BitOffsets", "PacketLength", "VideoTimestamp",
}

// directPlayBaseOrder is the report order of the direct play profile codes.
var directPlayBaseOrder = []string{PlaybackReasonContainer, PlaybackReasonVideoCodec, PlaybackReasonAudioCodec}

// DeviceProfileReasons reports why a client that declared profile (nil:
// nothing declared) and the bit rate ceiling maxBitrate (0: none) cannot
// play source as it is; nil when it can. Reasons come in a fixed order:
// container, video codec and audio codec of the direct play profiles, the
// container conditions, the codec conditions (in conditionProperties
// order), then bitrate_exceeds_client.
//
// Values the server does not know are not held against the source: an
// unprobed source's codecs pass the direct play profiles and its conditions
// are not evaluated, and an unknown bit rate passes the ceiling. A probed
// source's unknown property fails only a required condition.
//
// A multi-part version (G21.1) is playable only when every part is; its
// reasons are the union over the parts in the same order.
func DeviceProfileReasons(profile *DeviceProfile, maxBitrate int64, source PlaybackSource) []string {
	if len(source.Parts) > 1 {
		var all []string
		for _, part := range source.Parts {
			single := part.PlaybackSource
			single.Parts = nil
			for _, r := range DeviceProfileReasons(profile, maxBitrate, single) {
				if !slices.Contains(all, r) {
					all = append(all, r)
				}
			}
		}
		slices.SortStableFunc(all, func(a, b string) int { return reasonRank(a) - reasonRank(b) })
		return all
	}
	var reasons []string
	if profile != nil {
		reasons = append(reasons, directPlayProfileReasons(profile.DirectPlay, source)...)
		if source.Probed {
			values := sourceConditionValues(source)
			reasons = append(reasons, containerConditionReasons(profile.Containers, source, values)...)
			reasons = append(reasons, codecConditionReasons(profile.Codecs, source, values)...)
		}
	}
	if BitrateExceedsClient(maxBitrate, source) {
		reasons = append(reasons, PlaybackReasonBitrate)
	}
	return reasons
}

// BitrateExceedsClient reports a known bit rate strictly above a declared
// ceiling; an unknown bit rate or no ceiling never exceeds.
func BitrateExceedsClient(maxBitrate int64, source PlaybackSource) bool {
	return maxBitrate > 0 && source.BitRate != nil && *source.BitRate > maxBitrate
}

// reasonRank orders reasons for the multi-part union.
func reasonRank(reason string) int {
	if i := slices.Index(directPlayBaseOrder, reason); i >= 0 {
		return i
	}
	n := len(directPlayBaseOrder)
	for group, prefix := range []string{PlaybackReasonContainerCondition, PlaybackReasonCodecCondition} {
		if property, ok := strings.CutPrefix(reason, prefix+":"); ok {
			return n + group*len(conditionProperties) + slices.Index(conditionProperties, property)
		}
	}
	return n + 2*len(conditionProperties)
}

// directPlayProfileReasons explains the direct play profiles. Some video
// profile must accept the container, the primary video codec and the
// default audio codec; when none does, the reasons are those of the profile
// that fails the fewest checks (the first of equals), so the answer names
// what keeps the closest declaration from accepting the source. Without a
// video profile the container is not accepted.
func directPlayProfileReasons(profiles []DirectPlayProfile, source PlaybackSource) []string {
	var best []string
	found := false
	for _, p := range profiles {
		if !p.Video {
			continue
		}
		failed := directPlayProfileFailures(p, source)
		if len(failed) == 0 {
			return nil
		}
		if !found || len(failed) < len(best) {
			best, found = failed, true
		}
	}
	if !found {
		return []string{PlaybackReasonContainer}
	}
	return best
}

func directPlayProfileFailures(p DirectPlayProfile, source PlaybackSource) []string {
	caps, err := NormalizeClientCapabilities(ClientCapabilities{Containers: p.Containers, VideoCodecs: p.VideoCodecs, AudioCodecs: p.AudioCodecs})
	if err != nil {
		// A list the server cannot read declares nothing it can rely on.
		return slices.Clone(directPlayBaseOrder)
	}
	var failed []string
	if source.Container != "" && len(caps.Containers) > 0 && !slices.Contains(caps.Containers, source.Container) {
		failed = append(failed, PlaybackReasonContainer)
	}
	if video, ok := PrimaryVideoTrack(source); ok && video.Codec != "" && len(caps.VideoCodecs) > 0 && !supportedCodec(caps.VideoCodecs, video.Codec) {
		failed = append(failed, PlaybackReasonVideoCodec)
	}
	if audio, ok := DefaultAudioTrack(source); ok && audio.Codec != "" && len(caps.AudioCodecs) > 0 && !supportedCodec(caps.AudioCodecs, audio.Codec) {
		failed = append(failed, PlaybackReasonAudioCodec)
	}
	return failed
}

// PrimaryVideoTrack is the video stream decisions check.
func PrimaryVideoTrack(source PlaybackSource) (PlaybackVideoTrack, bool) {
	for _, v := range source.Video {
		if v.Primary {
			return v, true
		}
	}
	return PlaybackVideoTrack{}, false
}

// DefaultAudioTrack is the embedded audio stream a player starts with: the
// one the user's track preferences pick, else the default one, else the
// first.
func DefaultAudioTrack(source PlaybackSource) (PlaybackAudioTrack, bool) {
	if len(source.Audio) == 0 {
		return PlaybackAudioTrack{}, false
	}
	if tracks := source.DefaultTracks; tracks != nil && tracks.Audio != nil && tracks.Audio.Kind == TrackEmbedded && tracks.Audio.Index != nil {
		for _, audio := range source.Audio {
			if audio.Index == *tracks.Audio.Index {
				return audio, true
			}
		}
	}
	for _, audio := range source.Audio {
		if audio.Default {
			return audio, true
		}
	}
	return source.Audio[0], true
}

func containerConditionReasons(profiles []ContainerProfile, source PlaybackSource, values map[string]conditionValue) []string {
	var failed []string
	for _, p := range profiles {
		if !p.Video || !listAccepts(p.Containers, source.Container, playbackContainerAliases) {
			continue
		}
		failed = appendUnmet(failed, p.Conditions, values)
	}
	return conditionReasons(PlaybackReasonContainerCondition, failed)
}

func codecConditionReasons(profiles []CodecProfile, source PlaybackSource, values map[string]conditionValue) []string {
	video, hasVideo := PrimaryVideoTrack(source)
	audio, hasAudio := DefaultAudioTrack(source)
	var failed []string
	for _, p := range profiles {
		var applies bool
		switch p.Type {
		case CodecProfileVideo:
			applies = hasVideo && codecListAccepts(p.Codecs, video.Codec, playbackVideoAliases)
		case CodecProfileVideoAudio:
			applies = hasAudio && codecListAccepts(p.Codecs, audio.Codec, playbackAudioAliases)
		}
		// Audio profiles describe audio-only items, which the catalog
		// does not hold.
		if !applies || !listAccepts(p.Containers, source.Container, playbackContainerAliases) {
			continue
		}
		if len(appendUnmet(nil, p.ApplyConditions, values)) > 0 {
			continue
		}
		failed = appendUnmet(failed, p.Conditions, values)
	}
	return conditionReasons(PlaybackReasonCodecCondition, failed)
}

// appendUnmet adds the evaluated properties whose conditions do not hold.
func appendUnmet(failed []string, conditions []ProfileCondition, values map[string]conditionValue) []string {
	for _, c := range conditions {
		property, ok := conditionProperty(c.Property)
		if !ok || slices.Contains(failed, property) {
			continue
		}
		if !conditionHolds(c, values[property]) {
			failed = append(failed, property)
		}
	}
	return failed
}

func conditionReasons(prefix string, failed []string) []string {
	var out []string
	for _, property := range conditionProperties {
		if slices.Contains(failed, property) {
			out = append(out, prefix+":"+property)
		}
	}
	return out
}

// conditionProperty maps a client's property name to the evaluated name;
// unmeasured and unknown properties are not evaluated.
func conditionProperty(name string) (string, bool) {
	for _, p := range conditionProperties {
		if strings.EqualFold(p, strings.TrimSpace(name)) {
			return p, true
		}
	}
	return "", false
}

// conditionValue is one property of the source; known is false when the
// probe did not find it.
type conditionValue struct {
	known bool
	kind  byte // 'i' integer, 'f' decimal, 's' text, 'b' boolean
	i     int64
	f     float64
	s     []string // a text value and the values it also satisfies
	b     bool
}

func intValue(v *int64) conditionValue {
	if v == nil {
		return conditionValue{kind: 'i'}
	}
	return conditionValue{known: true, kind: 'i', i: *v}
}

func intKnown(v int64) conditionValue { return conditionValue{known: true, kind: 'i', i: v} }

func textValue(values ...string) conditionValue {
	if len(values) == 0 || values[0] == "" {
		return conditionValue{kind: 's'}
	}
	return conditionValue{known: true, kind: 's', s: values}
}

// sourceConditionValues collects the evaluated properties of a probed
// source: its primary video stream and its default audio stream.
func sourceConditionValues(source PlaybackSource) map[string]conditionValue {
	values := map[string]conditionValue{
		"NumVideoStreams": intKnown(int64(len(source.Video))),
		"NumAudioStreams": intKnown(int64(len(source.Audio))),
	}
	if video, ok := PrimaryVideoTrack(source); ok {
		values["VideoProfile"] = textValue(strings.TrimSpace(video.Profile))
		values["VideoLevel"] = intValue(video.Level)
		values["VideoBitDepth"] = intValue(VideoBitDepth(video))
		values["VideoRangeType"] = textValue(VideoRangeTypes(video)...)
		values["Width"] = intValue(video.Width)
		values["Height"] = intValue(video.Height)
		values["VideoBitrate"] = intValue(video.BitRate)
		values["VideoFramerate"] = conditionValue{kind: 'f'}
		if r := video.FrameRate; r != nil && r.Numerator > 0 && r.Denominator > 0 {
			values["VideoFramerate"] = conditionValue{known: true, kind: 'f', f: float64(r.Numerator) / float64(r.Denominator)}
		}
	}
	if audio, ok := DefaultAudioTrack(source); ok {
		values["AudioChannels"] = intValue(audio.Channels)
		values["AudioSampleRate"] = intValue(audio.SampleRate)
		values["AudioBitDepth"] = intValue(audio.BitDepth)
		values["AudioProfile"] = textValue(strings.TrimSpace(audio.Profile))
		values["AudioBitrate"] = intValue(audio.BitRate)
		// The checked stream is the one a player starts with.
		values["IsSecondaryAudio"] = conditionValue{known: true, kind: 'b', b: false}
	}
	return values
}

// conditionHolds evaluates one condition like upstream's condition
// processor: an unknown value holds unless the condition is required; a
// value or comparison the server cannot read does not hold.
func conditionHolds(c ProfileCondition, v conditionValue) bool {
	if !v.known {
		return !c.IsRequired
	}
	op := ""
	for _, name := range []string{ConditionEquals, ConditionNotEquals, ConditionLessThanEqual, ConditionGreaterThanEqual, ConditionEqualsAny} {
		if strings.EqualFold(name, strings.TrimSpace(c.Condition)) {
			op = name
		}
	}
	expected := []string{strings.TrimSpace(c.Value)}
	if op == ConditionEqualsAny {
		expected = strings.Split(c.Value, "|")
	}
	switch op {
	case ConditionEquals, ConditionEqualsAny:
		return slices.ContainsFunc(expected, func(e string) bool { return valueEquals(v, strings.TrimSpace(e)) })
	case ConditionNotEquals:
		cmp, ok := compareValue(v, expected[0])
		return ok && cmp != 0
	case ConditionLessThanEqual, ConditionGreaterThanEqual:
		if v.kind != 'i' && v.kind != 'f' {
			return false
		}
		cmp, ok := compareValue(v, expected[0])
		if !ok {
			return false
		}
		if op == ConditionLessThanEqual {
			return cmp <= 0
		}
		return cmp >= 0
	}
	return false
}

// valueEquals compares for Equals and EqualsAny; a text value also equals
// the values it falls back to (VideoRangeTypes).
func valueEquals(v conditionValue, expected string) bool {
	if v.kind == 's' {
		return slices.ContainsFunc(v.s, func(s string) bool { return strings.EqualFold(s, expected) })
	}
	cmp, ok := compareValue(v, expected)
	return ok && cmp == 0
}

// compareValue compares the source value with the declared one: -1, 0 or
// 1, and false when the declared value cannot be read as the property's
// type. Text compares without case; only equality is meaningful for it.
func compareValue(v conditionValue, expected string) (int, bool) {
	switch v.kind {
	case 'i':
		e, err := strconv.ParseInt(expected, 10, 64)
		if err != nil {
			return 0, false
		}
		switch {
		case v.i < e:
			return -1, true
		case v.i > e:
			return 1, true
		}
		return 0, true
	case 'f':
		e, err := strconv.ParseFloat(expected, 64)
		if err != nil {
			return 0, false
		}
		switch {
		case v.f < e:
			return -1, true
		case v.f > e:
			return 1, true
		}
		return 0, true
	case 'b':
		e, err := strconv.ParseBool(strings.ToLower(expected))
		if err != nil {
			return 0, false
		}
		if v.b == e {
			return 0, true
		}
		return 1, true
	case 's':
		if strings.EqualFold(v.s[0], expected) {
			return 0, true
		}
		return 1, true
	}
	return 0, false
}

// VideoBitDepth is the luma bit depth the stream's profile or HDR format
// implies, nil when neither tells: "Main 10" and "High 10" are 10 bit,
// "Main 12" 12 bit, the 8 bit-only H.264, HEVC and VP9 profiles 8 bit, and
// an HDR or Dolby Vision stream at least 10 bit.
func VideoBitDepth(v PlaybackVideoTrack) *int64 {
	depth := func(n int64) *int64 { return &n }
	profile := strings.ToLower(strings.TrimSpace(v.Profile))
	switch {
	case strings.Contains(profile, "12"):
		return depth(12)
	case strings.Contains(profile, "10"):
		return depth(10)
	case v.Codec == "h264" && slices.Contains([]string{"baseline", "constrained baseline", "main", "extended", "high"}, profile),
		v.Codec == "hevc" && slices.Contains([]string{"main", "main still picture"}, profile),
		v.Codec == "vp9" && slices.Contains([]string{"profile 0", "profile 1"}, profile):
		return depth(8)
	case len(v.HDR) > 0 || v.DolbyVisionProfile != nil:
		return depth(10)
	}
	return nil
}

// VideoRangeTypes names the stream's upstream VideoRangeType, followed by
// the type its base layer plays as for a player without the enhancement:
// a Dolby Vision stream with an HDR10, HDR10+, HLG or SDR compatible base
// layer also satisfies a declaration of that base type, and HDR10+ also
// satisfies HDR10. These are the same bytes; nothing is stripped. A
// stream without an HDR format is SDR.
func VideoRangeTypes(v PlaybackVideoTrack) []string {
	has := func(h VersionHDR) bool { return slices.Contains(v.HDR, string(h)) }
	hdr10 := has(VersionHDR10) || has(VersionHDRGeneric)
	if !has(VersionHDRDolbyVision) {
		switch {
		case has(VersionHDR10Plus):
			return []string{"HDR10Plus", "HDR10"}
		case hdr10:
			return []string{"HDR10"}
		case has(VersionHDRHLG):
			return []string{"HLG"}
		}
		return []string{"SDR"}
	}
	profile := int64(-1)
	if v.DolbyVisionProfile != nil {
		profile = *v.DolbyVisionProfile
	}
	switch {
	case profile == 5:
		return []string{"DOVI"}
	case profile == 7 && has(VersionHDR10Plus):
		return []string{"DOVIWithELHDR10Plus", "HDR10Plus", "HDR10"}
	case profile == 7:
		return []string{"DOVIWithEL", "HDR10"}
	case has(VersionHDR10Plus):
		return []string{"DOVIWithHDR10Plus", "HDR10Plus", "HDR10"}
	case hdr10:
		return []string{"DOVIWithHDR10", "HDR10"}
	case has(VersionHDRHLG):
		return []string{"DOVIWithHLG", "HLG"}
	case profile == 8 || profile == 9:
		return []string{"DOVIWithSDR", "SDR"}
	}
	return []string{"DOVI"}
}

// listAccepts reports whether a declared list accepts value; an empty list
// accepts everything and an unknown value is not held against the source.
func listAccepts(list []string, value string, aliases map[string]string) bool {
	if len(list) == 0 || value == "" {
		return true
	}
	return slices.Contains(profileTokens(list, aliases), value)
}

// codecListAccepts is listAccepts for codecs, where "pcm" covers every PCM
// sample format.
func codecListAccepts(list []string, codec string, aliases map[string]string) bool {
	if len(list) == 0 {
		return true
	}
	return supportedCodec(profileTokens(list, aliases), codec)
}

// profileTokens lower-cases and maps aliases without rejecting anything:
// a restriction applies however the client spells its other tokens.
func profileTokens(list []string, aliases map[string]string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		token := strings.ToLower(strings.TrimSpace(item))
		if alias, ok := aliases[token]; ok {
			token = alias
		}
		out = append(out, token)
	}
	return out
}

// SubtitleCandidate is one subtitle stream as the client would get it.
// Format is the stream's own format (a probe or file token); Deliverable is
// true when the server can serve it as a separate file as it is (an
// external file, or an embedded text track it has extracted).
type SubtitleCandidate struct {
	Format      string
	Language    string
	External    bool
	Deliverable bool
}

// SubtitleMethod chooses how the client gets a subtitle as it is: External
// when a profile accepts its format as an external file the server can
// serve, else Embed when it is inside the original and a profile accepts it
// embedded, else "" (subtitle_format_unsupported: the client declared no
// way to read it as it is). Encode, Hls and Drop profiles never match.
func SubtitleMethod(profiles []SubtitleProfile, container string, sub SubtitleCandidate) string {
	format := profileTokens([]string{sub.Format}, playbackSubtitleAliases)[0]
	accepts := func(method string) bool {
		return slices.ContainsFunc(profiles, func(p SubtitleProfile) bool {
			return strings.EqualFold(strings.TrimSpace(p.Method), method) &&
				profileTokens([]string{p.Format}, playbackSubtitleAliases)[0] == format &&
				listAccepts(p.Containers, container, playbackContainerAliases) &&
				(len(p.Languages) == 0 || sub.Language == "" || slices.ContainsFunc(p.Languages, func(l string) bool { return strings.EqualFold(strings.TrimSpace(l), sub.Language) }))
		})
	}
	switch {
	case sub.Deliverable && accepts(SubtitleMethodExternal):
		return SubtitleMethodExternal
	case !sub.External && accepts(SubtitleMethodEmbed):
		return SubtitleMethodEmbed
	}
	return ""
}
