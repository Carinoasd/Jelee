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
			title := strings.TrimSpace(text(track["Title"]))
			if err == nil && streams[order] && title != "" && domain.ValidMatroskaText(title, domain.MaxMatroskaTextBytes) &&
				!slices.ContainsFunc(result.StreamTitles, func(s domain.MediaStreamTitle) bool { return s.Index == order }) && len(result.StreamTitles) < 64 {
				result.StreamTitles = append(result.StreamTitles, domain.MediaStreamTitle{Index: order, Title: title})
			}
		}
	}
	if len(result.Chapters) == 0 && len(result.Attachments) == 0 && len(result.Tags) == 0 && len(result.StreamTitles) == 0 {
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
