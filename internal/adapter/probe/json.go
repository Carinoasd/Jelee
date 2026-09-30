package probe

import (
	"math"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ParseJSON accepts bounded ffprobe -show_format/-show_streams/-show_chapters
// JSON. It never executes a program, opens a path, or retains arbitrary tags.
func ParseJSON(data []byte) (domain.MediaMetadata, error) {
	root, err := readJSON(data)
	if err != nil {
		return domain.MediaMetadata{}, err
	}
	p := metadataParser{}
	result := domain.MediaMetadata{Streams: make([]domain.MediaStream, 0), Chapters: make([]domain.MediaChapter, 0)}
	format := p.object(root["format"])
	if names := p.text(format["format_name"]); names != nil {
		if len(*names) > 1024 {
			p.limit()
		} else {
			parts := strings.Split(*names, ",")
			if len(parts) > 16 {
				p.limit()
			} else {
				seen := make(map[string]bool)
				for _, name := range parts {
					if formatNames[name] && !seen[name] {
						result.Format.Names = append(result.Format.Names, name)
						seen[name] = true
					}
				}
			}
		}
	}
	result.Format.DurationMicros = p.micros(format["duration"])
	result.Format.SizeBytes = p.integer(format["size"], true, 0, math.MaxInt64)
	result.Format.BitRate = p.positive(format["bit_rate"], true, math.MaxInt64)
	streams := p.array(root["streams"], MaxStreams)
	if len(streams) == 0 && p.err == nil {
		p.invalid()
	}
	indexes := make(map[int]bool)
	for _, value := range streams {
		stream := p.stream(p.object(value))
		if indexes[stream.Index] {
			p.invalid()
		}
		indexes[stream.Index] = true
		result.Streams = append(result.Streams, stream)
	}
	ids := make(map[int64]bool)
	for _, value := range p.array(root["chapters"], MaxChapters) {
		chapter := p.chapter(p.object(value), result.Format.DurationMicros)
		if ids[chapter.ID] {
			p.invalid()
		}
		ids[chapter.ID] = true
		result.Chapters = append(result.Chapters, chapter)
	}
	if p.err != nil {
		return domain.MediaMetadata{}, p.err
	}
	return result, nil
}

func (p *metadataParser) stream(raw object) domain.MediaStream {
	var stream domain.MediaStream
	index := p.integer(raw["index"], false, 0, math.MaxInt32)
	kind := p.text(raw["codec_type"])
	if index == nil || kind == nil {
		p.invalid()
		return stream
	}
	switch *kind {
	case "video", "audio", "subtitle", "data", "attachment":
	default:
		p.invalid()
		return stream
	}
	stream.Index, stream.Kind = int(*index), *kind
	stream.Codec = p.enum(raw["codec_name"], codecNames)
	stream.Profile = p.enum(raw["profile"], profiles)
	stream.DurationMicros = p.micros(raw["duration"])
	stream.BitRate = p.positive(raw["bit_rate"], true, math.MaxInt64)
	disposition := p.object(raw["disposition"])
	stream.Default = p.flag(disposition["default"])
	stream.Forced = p.flag(disposition["forced"])
	tags := p.object(raw["tags"])
	if language := p.text(tags["language"]); language != nil && len(*language) <= 35 && languageTag.MatchString(*language) {
		normalized := strings.ToLower(*language)
		stream.Language = &normalized
	}
	switch stream.Kind {
	case "video":
		stream.Video = p.video(raw)
	case "audio":
		stream.Audio = &domain.MediaAudio{Channels: p.positive(raw["channels"], false, 256), ChannelLayout: p.enum(raw["channel_layout"], channelLayouts), SampleRate: p.positive(raw["sample_rate"], true, 768000), BitsPerSample: p.positive(raw["bits_per_sample"], false, 64), BitsPerRawSample: p.positive(raw["bits_per_raw_sample"], true, 64)}
		if stream.Codec != nil && stream.Profile != nil && (*stream.Codec == "eac3" && *stream.Profile == "Dolby Digital Plus + Dolby Atmos" || *stream.Codec == "truehd" && *stream.Profile == "Dolby TrueHD + Dolby Atmos") {
			yes := true
			stream.Audio.Atmos = &yes
		}
	}
	return stream
}

func (p *metadataParser) video(raw object) *domain.MediaVideo {
	video := &domain.MediaVideo{Width: p.positive(raw["width"], false, 65535), Height: p.positive(raw["height"], false, 65535), FrameRate: p.rate(raw["r_frame_rate"]), AverageFrameRate: p.rate(raw["avg_frame_rate"]), ColorRange: p.enum(raw["color_range"], colorsRange), ColorSpace: p.enum(raw["color_space"], colorsSpace), ColorTransfer: p.enum(raw["color_transfer"], colorsTransfer), ColorPrimaries: p.enum(raw["color_primaries"], colorsPrimaries)}
	level := p.integer(raw["level"], false, -99, 65535)
	if level != nil {
		if *level == -99 {
			level = nil
		} else if *level < 0 {
			p.invalid()
		}
	}
	video.Level = level
	seen := make(map[string]bool)
	for _, value := range p.array(raw["side_data_list"], MaxSideData) {
		side := p.object(value)
		name := p.text(side["side_data_type"])
		if name == nil {
			continue
		}
		switch *name {
		case "Mastering display metadata", "Content light level metadata", "DOVI configuration record", "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)":
			if seen[*name] {
				p.invalid()
				continue
			}
			seen[*name] = true
		default:
			continue
		}
		switch *name {
		case "Mastering display metadata":
			mastering := &domain.MediaMasteringDisplay{RedX: p.rational(side["red_x"], 1), RedY: p.rational(side["red_y"], 1), GreenX: p.rational(side["green_x"], 1), GreenY: p.rational(side["green_y"], 1), BlueX: p.rational(side["blue_x"], 1), BlueY: p.rational(side["blue_y"], 1), WhiteX: p.rational(side["white_point_x"], 1), WhiteY: p.rational(side["white_point_y"], 1), MinLuminance: p.rational(side["min_luminance"], 1000000), MaxLuminance: p.rational(side["max_luminance"], 1000000)}
			if mastering.MinLuminance != nil && mastering.MaxLuminance != nil && compareRational(mastering.MinLuminance, mastering.MaxLuminance) > 0 {
				p.invalid()
			}
			video.MasteringDisplay = mastering
		case "Content light level metadata":
			light := &domain.MediaContentLight{MaxContent: p.integer(side["max_content"], false, 0, 65535), MaxAverage: p.integer(side["max_average"], false, 0, 65535)}
			if light.MaxContent != nil && light.MaxAverage != nil && *light.MaxAverage > *light.MaxContent {
				p.invalid()
			}
			video.ContentLight = light
		case "DOVI configuration record":
			video.DolbyVision = &domain.MediaDolbyVision{Profile: p.integer(side["dv_profile"], false, 0, 255), Level: p.integer(side["dv_level"], false, 0, 255), RPU: p.flag(side["rpu_present_flag"]), EnhancementLayer: p.flag(side["el_present_flag"]), BaseLayer: p.flag(side["bl_present_flag"]), CompatibilityID: p.integer(side["dv_bl_signal_compatibility_id"], false, 0, 15)}
		case "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)":
			yes := true
			video.HDR10Plus = &yes
		}
	}
	return video
}

func (p *metadataParser) chapter(raw object, duration *int64) domain.MediaChapter {
	var result domain.MediaChapter
	id := p.integer(raw["id"], false, 0, math.MaxInt64)
	if id == nil {
		p.invalid()
	} else {
		result.ID = *id
	}
	base := p.rational(raw["time_base"], math.MaxInt64)
	if base != nil && base.Numerator == 0 {
		p.invalid()
	}
	start := p.integer(raw["start"], false, 0, math.MaxInt64)
	end := p.integer(raw["end"], false, 0, math.MaxInt64)
	startTime := p.micros(raw["start_time"])
	endTime := p.micros(raw["end_time"])
	fromStart := p.tickTime(start, base)
	fromEnd := p.tickTime(end, base)
	if startTime != nil && fromStart != nil && *startTime != *fromStart {
		p.invalid()
	}
	if endTime != nil && fromEnd != nil && *endTime != *fromEnd {
		p.invalid()
	}
	if startTime == nil {
		startTime = fromStart
	}
	if endTime == nil {
		endTime = fromEnd
	}
	if startTime != nil && endTime != nil && *startTime > *endTime {
		p.invalid()
	}
	if duration != nil && (startTime != nil && *startTime > *duration || endTime != nil && *endTime > *duration) {
		p.invalid()
	}
	result.StartMicros, result.EndMicros = startTime, endTime
	return result
}
