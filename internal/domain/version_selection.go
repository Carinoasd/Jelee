package domain

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Default version choice (G20.2). The versions of an item are ordered in
// three steps:
//
//  1. Client capabilities. A caller that knows what the client declared
//     (its containers and codecs, its bit rate ceiling) excludes every
//     version the client cannot play as it is. A declaration only chooses
//     among originals; it never makes a conversion acceptable (ADR 0007).
//  2. The user's bandwidth preference: versions whose known bit rate fits
//     come first, then the others by ascending bit rate.
//  3. The user's other preferences, in this order: a resolution ceiling
//     (fitting versions first, then the smaller ones), the dynamic range
//     (SDR or HDR) and smaller files.
//
// Equal versions keep the established order: the administrator's main
// version, then the quality score, then the source ID. Preferences are soft:
// they reorder, they never hide a version the client can play.

// Version preference dynamic ranges.
const (
	VersionRangeSDR = "sdr"
	VersionRangeHDR = "hdr"
)

// VersionPreferenceHeights are the accepted resolution ceilings.
var VersionPreferenceHeights = []int{480, 576, 720, 1080, 1440, 2160, 4320}

// Selection reason codes. VersionSelection.Reason names the rule that put
// the default version ahead of the next playable one.
const (
	// VersionReasonOnlyVersion means the item has a single version.
	VersionReasonOnlyVersion = "only_version"
	// VersionReasonOnlyPlayable means every other version was excluded by the
	// client's declared capabilities.
	VersionReasonOnlyPlayable = "only_playable"
	VersionReasonBandwidth    = "bandwidth_preference"
	VersionReasonResolution   = "resolution_preference"
	VersionReasonRange        = "dynamic_range_preference"
	VersionReasonSize         = "size_preference"
	VersionReasonPrimary      = "primary_version"
	VersionReasonQuality      = "quality_score"
	// VersionReasonStableOrder means nothing tells the versions apart; the
	// source ID decides so the choice is stable.
	VersionReasonStableOrder = "stable_order"
	// VersionReasonNonePlayable means no version is playable as it is; there is
	// no default and the caller keeps its usual error.
	VersionReasonNonePlayable = "none_playable"
	// VersionReasonNoVersion means the item has no version at all.
	VersionReasonNoVersion = "no_version"
)

// VersionPreference is a user's version choice preference. Every member is
// optional; nil leaves the rule out.
type VersionPreference struct {
	// MaxHeight is a resolution ceiling by tier (1080 keeps 1080p, so a
	// 1920x800 scope picture fits); one of VersionPreferenceHeights.
	MaxHeight *int `json:"maxHeight"`
	// DynamicRange prefers SDR or HDR versions (HDR covers HDR10, HDR10+,
	// HLG and Dolby Vision).
	DynamicRange *string `json:"dynamicRange"`
	// PreferSmaller prefers smaller files when set to true.
	PreferSmaller *bool `json:"preferSmaller"`
	// MaxBitrate is the user's bandwidth ceiling in bits per second.
	MaxBitrate *int64 `json:"maxBitrate"`
}

// Empty reports whether no member is set; an empty preference is stored as
// no row.
func (p VersionPreference) Empty() bool {
	return p.MaxHeight == nil && p.DynamicRange == nil && p.PreferSmaller == nil && p.MaxBitrate == nil
}

// NormalizeVersionPreference validates a preference.
func NormalizeVersionPreference(p VersionPreference) (VersionPreference, error) {
	if p.MaxHeight != nil && !slices.Contains(VersionPreferenceHeights, *p.MaxHeight) {
		return VersionPreference{}, ErrInvalid
	}
	if p.DynamicRange != nil && *p.DynamicRange != VersionRangeSDR && *p.DynamicRange != VersionRangeHDR {
		return VersionPreference{}, ErrInvalid
	}
	if p.MaxBitrate != nil && (*p.MaxBitrate < 1 || *p.MaxBitrate > ClientCapabilityBitrateMax) {
		return VersionPreference{}, ErrInvalid
	}
	return p, nil
}

// ExcludedVersion is one version the client's declaration excluded, with
// the direct play reason codes.
type ExcludedVersion struct {
	SourceID string   `json:"sourceId"`
	Reasons  []string `json:"reasons"`
}

// VersionSelection explains the default version.
type VersionSelection struct {
	// DefaultSourceID is empty when no version is playable.
	DefaultSourceID string `json:"defaultSourceId,omitempty"`
	// Reason is one of the VersionReason codes: with several playable
	// versions, the rule of highest precedence that put the default ahead
	// of another playable version.
	Reason string `json:"reason"`
	// Preferences lists the user preference rules that were set
	// (bandwidth, resolution, dynamic_range, size), whether or not they
	// told the versions apart.
	Preferences []string `json:"preferences"`
	// Excluded lists the versions the client's declaration excluded.
	Excluded []ExcludedVersion `json:"excluded"`
}

// versionRank holds the preference keys of one version; zero values tie.
type versionRank struct {
	overBitrate, overHeight bool
	bitrate, height         int64
	rangeMismatch           bool
	size                    int64
}

func rankVersion(source PlaybackSource, p VersionPreference) versionRank {
	var r versionRank
	if p.MaxBitrate != nil && source.BitRate != nil && *source.BitRate > *p.MaxBitrate {
		// An unknown bit rate is not held against a version.
		r.overBitrate, r.bitrate = true, *source.BitRate
	}
	if p.MaxHeight != nil {
		if h := VersionHeight(source); h > int64(*p.MaxHeight) {
			r.overHeight, r.height = true, h
		}
	}
	if p.DynamicRange != nil {
		hdr := len(source.Version.HDR) > 0
		r.rangeMismatch = hdr != (*p.DynamicRange == VersionRangeHDR)
	}
	if p.PreferSmaller != nil && *p.PreferSmaller {
		r.size = 1<<62 - 1
		if source.SizeBytes != nil {
			r.size = *source.SizeBytes
		}
	}
	return r
}

// VersionHeight is the resolution tier of a version as a height (1080 for
// 1080p, 480 for SD); zero when unknown.
func VersionHeight(source PlaybackSource) int64 {
	tier := string(source.Version.Resolution)
	if tier == string(VersionResolutionSD) {
		return 480
	}
	h, err := strconv.ParseInt(strings.TrimSuffix(tier, "p"), 10, 64)
	if err != nil || !strings.HasSuffix(tier, "p") {
		return 0
	}
	return h
}

// versionRules are the comparison rules in precedence order with their
// reason codes.
var versionRules = []struct {
	reason  string
	compare func(a, b PlaybackSource, ra, rb versionRank) int
}{
	{VersionReasonBandwidth, func(_, _ PlaybackSource, ra, rb versionRank) int {
		return cmp.Or(boolOrder(ra.overBitrate, rb.overBitrate), cmp.Compare(ra.bitrate, rb.bitrate))
	}},
	{VersionReasonResolution, func(_, _ PlaybackSource, ra, rb versionRank) int {
		return cmp.Or(boolOrder(ra.overHeight, rb.overHeight), cmp.Compare(ra.height, rb.height))
	}},
	{VersionReasonRange, func(_, _ PlaybackSource, ra, rb versionRank) int {
		return boolOrder(ra.rangeMismatch, rb.rangeMismatch)
	}},
	{VersionReasonSize, func(_, _ PlaybackSource, ra, rb versionRank) int { return cmp.Compare(ra.size, rb.size) }},
	{VersionReasonPrimary, func(a, b PlaybackSource, _, _ versionRank) int { return boolOrder(b.Primary, a.Primary) }},
	{VersionReasonQuality, func(a, b PlaybackSource, _, _ versionRank) int {
		return cmp.Compare(b.Version.QualityScore, a.Version.QualityScore)
	}},
	{VersionReasonStableOrder, func(a, b PlaybackSource, _, _ versionRank) int { return cmp.Compare(a.ID, b.ID) }},
}

// boolOrder sorts false before true.
func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// compareRanked compares two versions and reports the deciding rule.
func compareRanked(a, b PlaybackSource, ra, rb versionRank) (int, string) {
	for _, rule := range versionRules {
		if c := rule.compare(a, b, ra, rb); c != 0 {
			return c, rule.reason
		}
	}
	return 0, VersionReasonStableOrder
}

// CompareVersions is the established order without preferences: the main
// version, then quality score descending, then source ID.
func CompareVersions(a, b PlaybackSource) int {
	c, _ := compareRanked(a, b, versionRank{}, versionRank{})
	return c
}

// SortVersions orders versions for a user's preference in place.
func SortVersions(sources []PlaybackSource, p VersionPreference) {
	ranks := make(map[string]versionRank, len(sources))
	for _, s := range sources {
		ranks[s.ID] = rankVersion(s, p)
	}
	slices.SortStableFunc(sources, func(a, b PlaybackSource) int {
		c, _ := compareRanked(a, b, ranks[a.ID], ranks[b.ID])
		return c
	})
}

// SelectVersion orders versions for a user's preference in place and picks
// the default: the first version exclude does not reject. exclude returns
// the direct play reason codes of a version the client cannot play as it
// is; nil keeps every version.
func SelectVersion(sources []PlaybackSource, p VersionPreference, exclude func(PlaybackSource) []string) VersionSelection {
	SortVersions(sources, p)
	sel := VersionSelection{Preferences: versionPreferenceRules(p), Excluded: []ExcludedVersion{}}
	var playable []PlaybackSource
	for _, s := range sources {
		if exclude != nil {
			if reasons := exclude(s); len(reasons) > 0 {
				sel.Excluded = append(sel.Excluded, ExcludedVersion{SourceID: s.ID, Reasons: reasons})
				continue
			}
		}
		playable = append(playable, s)
	}
	switch {
	case len(sources) == 0:
		sel.Reason = VersionReasonNoVersion
	case len(playable) == 0:
		sel.Reason = VersionReasonNonePlayable
	case len(sources) == 1:
		sel.DefaultSourceID, sel.Reason = playable[0].ID, VersionReasonOnlyVersion
	case len(playable) == 1:
		sel.DefaultSourceID, sel.Reason = playable[0].ID, VersionReasonOnlyPlayable
	default:
		sel.DefaultSourceID, sel.Reason = playable[0].ID, decidingRule(playable, p)
	}
	return sel
}

// decidingRule is the rule of highest precedence that put the default
// (playable[0]) ahead of any other playable version.
func decidingRule(playable []PlaybackSource, p VersionPreference) string {
	first := rankVersion(playable[0], p)
	best := len(versionRules) - 1
	for _, other := range playable[1:] {
		_, reason := compareRanked(playable[0], other, first, rankVersion(other, p))
		for i, rule := range versionRules {
			if rule.reason == reason && i < best {
				best = i
			}
		}
	}
	return versionRules[best].reason
}

// versionPreferenceRules names the preference rules that are set.
func versionPreferenceRules(p VersionPreference) []string {
	out := []string{}
	if p.MaxBitrate != nil {
		out = append(out, "bandwidth")
	}
	if p.MaxHeight != nil {
		out = append(out, "resolution")
	}
	if p.DynamicRange != nil {
		out = append(out, "dynamic_range")
	}
	if p.PreferSmaller != nil && *p.PreferSmaller {
		out = append(out, "size")
	}
	return out
}

// DirectPlayExclusion excludes the versions a capability declaration cannot
// play as it is (DecideDirectPlay); it never accepts a conversion.
func DirectPlayExclusion(caps ClientCapabilities) func(PlaybackSource) []string {
	normalized, err := NormalizeClientCapabilities(caps)
	if err != nil {
		normalized = ClientCapabilities{}
	}
	return func(s PlaybackSource) []string {
		ok, reasons := DecideDirectPlay(normalized, s)
		if ok {
			return nil
		}
		return reasons
	}
}
