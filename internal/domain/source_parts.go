package domain

import (
	"cmp"
	"slices"
)

// Multi-part versions (G21.1). A film or an episode split over several
// files (movie.cd1.mkv, movie.cd2.mkv) is one version whose parts are played
// one after the other. Nothing is joined or converted (ADR 0007): every part
// stays an original file delivered as it is, and the first part is the
// version itself, so a client that knows nothing about parts plays part 1.

// Part outcomes stored per scan source (catalog_scan_sources.part_reason).
// Only files whose name carries a part marker get one.
const (
	// SourcePartMerged: the file is part of a multi-part version.
	SourcePartMerged = "merged"
	// SourcePartSingle: no other file of the same directory differs from it
	// only in the part number.
	SourcePartSingle = "single"
	// SourcePartGap: the part numbers of its files do not run 1..n.
	SourcePartGap = "gap"
	// SourcePartDuplicate: two files claim the same part number.
	SourcePartDuplicate = "duplicate"
	// SourcePartMixed: the part numbers only run 1..n across files that
	// differ in more than the part number (720p cd1 + 1080p cd2), which
	// are different versions, so none of them is merged.
	SourcePartMixed = "mixed"
	// SourcePartHeld: an administrator decided about the item's versions;
	// its parts are left exactly as they are.
	SourcePartHeld = "manual"
)

// SourcePartsMax bounds the parts of one version. The parser reads at most
// two-digit part numbers.
const SourcePartsMax = 99

// SourcePartCandidate is one scan-registered file of an item. Part and Stem
// come from the file name (medianame.Parsed.Part and PartStem); Directory is
// the file's library-relative directory.
type SourcePartCandidate struct {
	SourceID  string
	Directory string
	Stem      string
	Part      int
}

// SourcePartPlan is the multi-part structure of one item.
type SourcePartPlan struct {
	// Versions lists every multi-part version as its sources in part
	// order; the first one is the version.
	Versions [][]string
	// Reasons holds the outcome of every candidate with a part marker.
	Reasons map[string]string
}

// PlanSourceParts groups an item's files into multi-part versions. Files
// form one version exactly when they share the directory and the part stem
// (the name without the part marker, so the same title, year, release tags
// and container) and their part numbers are exactly 1..n with n >= 2.
// Anything else (a gap, a missing first part, a duplicated number, a single
// file, files that only complete each other across different release tags)
// is not merged: each file stays a version of its own and its outcome says
// why. The plan only depends on its input set, never on order.
func PlanSourceParts(candidates []SourcePartCandidate) SourcePartPlan {
	plan := SourcePartPlan{Reasons: map[string]string{}}
	type key struct{ directory, stem string }
	clusters := map[key][]SourcePartCandidate{}
	for _, c := range candidates {
		if c.Part <= 0 || c.Part > SourcePartsMax || c.Stem == "" || c.SourceID == "" {
			continue
		}
		k := key{c.Directory, c.Stem}
		clusters[k] = append(clusters[k], c)
	}
	keys := make([]key, 0, len(clusters))
	for k := range clusters {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int { return cmp.Or(cmp.Compare(a.directory, b.directory), cmp.Compare(a.stem, b.stem)) })
	// Unmerged part numbers per directory, to tell a mixed set apart.
	leftover := map[string][]SourcePartCandidate{}
	for _, k := range keys {
		members := clusters[k]
		slices.SortFunc(members, func(a, b SourcePartCandidate) int {
			return cmp.Or(cmp.Compare(a.Part, b.Part), cmp.Compare(a.SourceID, b.SourceID))
		})
		reason := SourcePartMerged
		switch {
		case len(members) == 1:
			reason = SourcePartSingle
		case !distinctParts(members):
			reason = SourcePartDuplicate
		case !consecutiveParts(members):
			reason = SourcePartGap
		}
		if reason == SourcePartMerged {
			version := make([]string, len(members))
			for i, m := range members {
				version[i] = m.SourceID
			}
			plan.Versions = append(plan.Versions, version)
		} else if reason != SourcePartDuplicate {
			leftover[k.directory] = append(leftover[k.directory], members...)
		}
		for _, m := range members {
			plan.Reasons[m.SourceID] = reason
		}
	}
	for _, members := range leftover {
		slices.SortFunc(members, func(a, b SourcePartCandidate) int { return cmp.Compare(a.Part, b.Part) })
		if len(members) > 1 && distinctParts(members) && consecutiveParts(members) {
			for _, m := range members {
				plan.Reasons[m.SourceID] = SourcePartMixed
			}
		}
	}
	return plan
}

// distinctParts reports sorted members without a repeated part number.
func distinctParts(members []SourcePartCandidate) bool {
	for i := 1; i < len(members); i++ {
		if members[i].Part == members[i-1].Part {
			return false
		}
	}
	return true
}

// consecutiveParts reports sorted, distinct members numbered exactly 1..n.
func consecutiveParts(members []SourcePartCandidate) bool {
	for i, m := range members {
		if m.Part != i+1 {
			return false
		}
	}
	return true
}

// PlaybackPart is one file of a multi-part version, described like a
// version of its own so a client can play it directly.
type PlaybackPart struct {
	// Number is the 1-based part number; part 1 is the version itself.
	Number int `json:"number"`
	// StartMicros is where the part begins on the version's combined
	// timeline (the sum of the earlier parts' durations); absent when an
	// earlier part's duration is unknown.
	StartMicros *int64 `json:"startMicros,omitempty"`
	PlaybackSource
}

// BuildPlaybackSources projects stored records into versions: the later
// parts of a multi-part version are folded into Parts of its first part and
// do not appear as versions of their own. Parts whose first part is not
// among the records stay separate versions, so nothing is ever hidden.
func BuildPlaybackSources(records []PlaybackSourceRecord) []PlaybackSource {
	firsts := map[string]bool{}
	for _, r := range records {
		if r.PartOf != "" && r.PartOf == r.ID && r.PartNumber == 1 {
			firsts[r.ID] = true
		}
	}
	sources := make([]PlaybackSource, 0, len(records))
	parts := map[string][]PlaybackPart{}
	for _, r := range records {
		s := BuildPlaybackSource(r)
		if r.PartOf == "" || r.PartNumber < 1 || !firsts[r.PartOf] {
			sources = append(sources, s)
			continue
		}
		parts[r.PartOf] = append(parts[r.PartOf], PlaybackPart{Number: r.PartNumber, PlaybackSource: s})
		if r.PartOf == r.ID {
			sources = append(sources, s)
		}
	}
	for i := range sources {
		list := parts[sources[i].ID]
		if len(list) < 2 {
			continue
		}
		slices.SortFunc(list, func(a, b PlaybackPart) int { return cmp.Compare(a.Number, b.Number) })
		var start int64
		known := true
		for j := range list {
			if known {
				offset := start
				list[j].StartMicros = &offset
			}
			d := list[j].DurationMicros
			if d == nil || *d <= 0 {
				known = false
				continue
			}
			start += *d
		}
		sources[i].Parts = list
		if known {
			total := start
			sources[i].TotalDurationMicros = &total
		}
	}
	return sources
}

// PartCount is the number of files a client plays for the version: 1 for a
// single file.
func (s PlaybackSource) PartCount() int {
	if len(s.Parts) > 1 {
		return len(s.Parts)
	}
	return 1
}

// FindPart returns the part with the given source ID: a version among
// sources, or a later part of one. ok is false when no version or part has
// the ID.
func FindPart(sources []PlaybackSource, sourceID string) (version PlaybackSource, part PlaybackPart, ok bool) {
	for _, s := range sources {
		if s.ID == sourceID {
			return s, PlaybackPart{Number: 1, StartMicros: new(int64), PlaybackSource: s}, true
		}
		for _, p := range s.Parts {
			if p.ID == sourceID {
				return s, p, true
			}
		}
	}
	return PlaybackSource{}, PlaybackPart{}, false
}

// RuntimeMicros is the playback runtime of a version: the combined duration
// of its parts, or the file's own duration for a single file. Nil when any
// part's duration is unknown.
func (s PlaybackSource) RuntimeMicros() *int64 {
	if len(s.Parts) > 1 {
		return s.TotalDurationMicros
	}
	return s.DurationMicros
}
