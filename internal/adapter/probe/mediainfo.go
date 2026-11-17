package probe

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MaxMediaInfoOutput bounds the MediaInfo JSON the supplement parser reads.
const MaxMediaInfoOutput = 4 << 20

// mediaInfoGeneralTags are the General fields kept as container tags, in
// output order. File system fields (names, dates, sizes) are never kept.
var mediaInfoGeneralTags = []string{"Title", "Movie", "Collection", "Season", "Part", "Director", "Genre", "Recorded_Date", "Encoded_Date", "Encoded_Application", "Encoded_Library", "Comment", "Description", "Copyright"}

// mediaInfoSkippedExtra are General "extra" members that are not tags.
var mediaInfoSkippedExtra = map[string]bool{"Attachments": true, "ErrorDetectionType": true, "FileExtension_Invalid": true}

var mediaInfoTagName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

var mediaInfoChapterKey = regexp.MustCompile(`^_(\d{2})_(\d{2})_(\d{2})_(\d{3})$`)

// ParseMediaInfo reads MediaInfo's JSON output into the Matroska supplement.
// streams are the probe stream indices that a track title may refer to. It
// returns nil when the output holds nothing to keep or is not usable; an
// unusable supplement never fails the probe it accompanies.
func ParseMediaInfo(data []byte, streams map[int]bool, duration *int64) *domain.MediaMatroska {
	if len(data) == 0 || len(data) > MaxMediaInfoOutput {
		return nil
	}
	var document struct {
		Media struct {
			Track []map[string]json.RawMessage `json:"track"`
		} `json:"media"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&document) != nil || len(document.Media.Track) > 512 {
		return nil
	}
	result := &domain.MediaMatroska{}
	menu := false
	for _, track := range document.Media.Track {
		switch text(track["@type"]) {
		case "General":
			result.Tags = generalTags(track)
			result.Attachments = attachments(track)
		case "Menu":
			// Only the first edition's chapters, like the probe's own list.
			if !menu {
				menu = true
				result.Chapters = chapters(track, duration)
			}
		case "Video", "Audio", "Text":
			order, err := strconv.Atoi(text(track["StreamOrder"]))
			if detail, ok := streamDetail(track); ok && err == nil && streams[order] && len(result.StreamDetails) < domain.MaxMatroskaStreamDetails &&
				!slices.ContainsFunc(result.StreamDetails, func(d domain.MediaStreamDetail) bool { return d.Index == order }) {
				detail.Index = order
				result.StreamDetails = append(result.StreamDetails, detail)
			}
			title := strings.TrimSpace(text(track["Title"]))
			if err == nil && streams[order] && title != "" && domain.ValidMatroskaText(title, domain.MaxMatroskaTextBytes) &&
				!slices.ContainsFunc(result.StreamTitles, func(s domain.MediaStreamTitle) bool { return s.Index == order }) && len(result.StreamTitles) < 64 {
				result.StreamTitles = append(result.StreamTitles, domain.MediaStreamTitle{Index: order, Title: title})
			}
		}
	}
	if len(result.Chapters) == 0 && len(result.Attachments) == 0 && len(result.Tags) == 0 && len(result.StreamTitles) == 0 && len(result.StreamDetails) == 0 {
		return nil
	}
	// The whole supplement must pass the stored whitelist; otherwise none of
	// it is kept, so a partial document is never mistaken for a complete one.
	if !domain.ValidMediaMatroska(result, duration, streams) {
		return nil
	}
	return result
}

func text(raw json.RawMessage) string {
	var value string
	if raw == nil || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func generalTags(track map[string]json.RawMessage) []domain.MediaTag {
	var tags []domain.MediaTag
	add := func(name, value string) {
		value = strings.TrimSpace(value)
		if len(tags) >= domain.MaxMatroskaTags || value == "" || !domain.ValidMatroskaText(value, domain.MaxMatroskaTextBytes) ||
			slices.ContainsFunc(tags, func(t domain.MediaTag) bool { return t.Name == name }) {
			return
		}
		tags = append(tags, domain.MediaTag{Name: name, Value: value})
	}
	for _, name := range mediaInfoGeneralTags {
		add(name, text(track[name]))
	}
	var extra map[string]json.RawMessage
	if raw := track["extra"]; raw != nil && json.Unmarshal(raw, &extra) == nil && len(extra) <= 256 {
		names := make([]string, 0, len(extra))
		for name := range extra {
			if !mediaInfoSkippedExtra[name] && mediaInfoTagName.MatchString(name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			add(name, text(extra[name]))
		}
	}
	return tags
}

func attachments(track map[string]json.RawMessage) []domain.MediaAttachment {
	var extra map[string]json.RawMessage
	if raw := track["extra"]; raw == nil || json.Unmarshal(raw, &extra) != nil {
		return nil
	}
	list := text(extra["Attachments"])
	if list == "" {
		return nil
	}
	names := strings.Split(list, " / ")
	if len(names) > domain.MaxMatroskaAttachments {
		return nil
	}
	out := make([]domain.MediaAttachment, 0, len(names))
	for i, name := range names {
		// IDs are positions; one unusable name would shift every later ID.
		if !domain.ValidAttachmentFileName(name) {
			return nil
		}
		out = append(out, domain.MediaAttachment{ID: i + 1, FileName: name, Font: domain.IsFontFileName(name)})
	}
	return out
}

func chapters(track map[string]json.RawMessage, duration *int64) []domain.MediaNamedChapter {
	var extra map[string]json.RawMessage
	if raw := track["extra"]; raw == nil || json.Unmarshal(raw, &extra) != nil || len(extra) > 4*domain.MaxMatroskaChapters {
		return nil
	}
	var out []domain.MediaNamedChapter
	for key, raw := range extra {
		match := mediaInfoChapterKey.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		h, _ := strconv.ParseInt(match[1], 10, 64)
		m, _ := strconv.ParseInt(match[2], 10, 64)
		s, _ := strconv.ParseInt(match[3], 10, 64)
		ms, _ := strconv.ParseInt(match[4], 10, 64)
		if m > 59 || s > 59 {
			return nil
		}
		start := ((h*60+m)*60+s)*1_000_000 + ms*1000
		if duration != nil && start > *duration {
			return nil
		}
		title := strings.TrimSpace(text(raw))
		if !domain.ValidMatroskaText(title, domain.MaxMatroskaTextBytes) {
			title = ""
		}
		out = append(out, domain.MediaNamedChapter{StartMicros: start, Title: title})
	}
	if len(out) > domain.MaxMatroskaChapters {
		return nil
	}
	slices.SortStableFunc(out, func(a, b domain.MediaNamedChapter) int {
		switch {
		case a.StartMicros < b.StartMicros:
			return -1
		case a.StartMicros > b.StartMicros:
			return 1
		}
		return strings.Compare(a.Title, b.Title)
	})
	return out
}

var mediaInfoDolbyVisionProfile = regexp.MustCompile(`(?i)dv[a-z0-9]{2,4}\.(\d{2})(?:\.(\d{2}))?`)

// streamDetail reads the encoding details of one Video or Audio track. The
// result carries tokens from fixed tables only; Index is set by the caller.
func streamDetail(track map[string]json.RawMessage) (domain.MediaStreamDetail, bool) {
	var d domain.MediaStreamDetail
	switch text(track["@type"]) {
	case "Video":
		d.HDR, d.DolbyVisionProfile, d.DolbyVisionLevel = mediaInfoHDR(track)
	case "Audio":
		d.AudioFeatures = mediaInfoAudioFeatures(track)
	default:
		return d, false
	}
	if profile := strings.TrimSpace(text(track["Format_Profile"])); profile != "" && domain.ValidMatroskaText(profile, domain.MaxMatroskaFormatProfile) {
		d.FormatProfile = profile
	}
	return d, len(d.HDR) > 0 || len(d.AudioFeatures) > 0 || d.FormatProfile != ""
}

// mediaInfoHDR classifies the HDR_Format* fields. Dolby Vision is reported
// with the profile and level of its "dvhe.08.06" style profile string; the
// base layer's HDR10 or HLG fallback comes from the compatibility field.
func mediaInfoHDR(track map[string]json.RawMessage) (tokens []string, profile, level *int64) {
	format := strings.ToLower(text(track["HDR_Format"]))
	compatibility := strings.ToLower(text(track["HDR_Format_Compatibility"]))
	transfer := strings.ToLower(text(track["transfer_characteristics"]))
	var found [4]bool
	if strings.Contains(format, "dolby vision") {
		found[0] = true
		if match := mediaInfoDolbyVisionProfile.FindStringSubmatch(text(track["HDR_Format_Profile"])); match != nil {
			p, _ := strconv.ParseInt(match[1], 10, 64)
			profile = &p
			levelText := match[2]
			if levelText == "" {
				levelText = text(track["HDR_Format_Level"])
			}
			if l, err := strconv.ParseInt(strings.TrimSpace(levelText), 10, 64); err == nil && l >= 0 && l <= 255 {
				level = &l
			}
		}
	}
	for _, part := range strings.FieldsFunc(compatibility+" / "+format, func(r rune) bool { return r == '/' || r == ',' }) {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "hdr10+") || strings.Contains(part, "smpte st 2094 app 4"):
			found[1] = true
		case part == "hdr10" || strings.HasPrefix(part, "hdr10 ") || strings.Contains(part, "smpte st 2086"):
			found[2] = true
		case part == "hlg" || strings.HasPrefix(part, "hlg ") || strings.Contains(part, "arib std-b67"):
			found[3] = true
		}
	}
	if strings.Contains(transfer, "hlg") || strings.Contains(transfer, "arib std-b67") {
		found[3] = true
	}
	for i, name := range []domain.VersionHDR{domain.VersionHDRDolbyVision, domain.VersionHDR10Plus, domain.VersionHDR10, domain.VersionHDRHLG} {
		if found[i] {
			tokens = append(tokens, string(name))
		}
	}
	if len(tokens) == 0 {
		return nil, nil, nil
	}
	return tokens, profile, level
}

// mediaInfoAudioFeatures classifies commercial audio formats from Format,
// Format_Commercial_IfAny, Format_AdditionalFeatures and Format_Profile.
func mediaInfoAudioFeatures(track map[string]json.RawMessage) []string {
	format := strings.ToLower(strings.TrimSpace(text(track["Format"])))
	commercial := strings.ToLower(text(track["Format_Commercial_IfAny"]))
	features := strings.Fields(strings.ToLower(text(track["Format_AdditionalFeatures"])))
	profile := strings.ToLower(strings.TrimSpace(text(track["Format_Profile"])))
	dts := format == "dts" || strings.Contains(commercial, "dts")
	truehd := format == "mlp fba" || strings.Contains(commercial, "truehd")
	atmos := strings.Contains(commercial, "atmos") || strings.Contains(profile, "atmos") ||
		// Joint object coding is how E-AC-3 carries Atmos.
		(format == "e-ac-3" || format == "mlp fba") && slices.Contains(features, "joc")
	dtsx := dts && (slices.Contains(features, "x") || strings.Contains(commercial, "dts:x") || strings.Contains(commercial, "dts-x"))
	// DTS:X rides on a lossless DTS-HD MA core; XLL is the lossless extension.
	dtsma := dts && (dtsx || slices.Contains(features, "xll") || strings.Contains(commercial, "master audio") || profile == "ma")
	var out []string
	for _, f := range []struct {
		on   bool
		name string
	}{{atmos, domain.AudioFeatureAtmos}, {truehd, domain.AudioFeatureTrueHD}, {dtsx, domain.AudioFeatureDTSX}, {dtsma, domain.AudioFeatureDTSHD}} {
		if f.on {
			out = append(out, f.name)
		}
	}
	return out
}
