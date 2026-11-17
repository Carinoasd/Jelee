package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// Automatic version grouping (G20.1, G20.5, G21.2). Synchronisation groups a
// new file with the files of the same directory and parsed identity. Copies
// of one movie or episode kept in different directories become separate
// items; once their NFO provider IDs are known, the versions phase of the
// next synchronisation folds them together when the evidence is strong
// enough. It never splits an item and never touches an item an administrator
// decided about.

// CatalogGroupingVersion is stored as part of every item's grouping evidence.
// Changing the grouping rules must change it, so every item of every library
// is judged again on its next synchronisation. Judging again only adds
// aggregation: registered versions are never moved apart.
const CatalogGroupingVersion = "grouping-v1"

// VersionGroupingProviders are the providers whose IDs name one work. Other
// provider types still count for conflicts, never as the shared evidence.
var VersionGroupingProviders = []string{"imdb", "tmdb", "tvdb"}

// Duration tolerance of two versions of one work: 2% of the longer runtime,
// at least 30 seconds (encoder and container rounding, a missing logo card)
// and at most 3 minutes (beyond that it is another cut, which an
// administrator may still merge by hand).
const (
	versionDurationFloorMicros   = int64(30_000_000)
	versionDurationCeilingMicros = int64(180_000_000)
)

// Grouping reasons. A merge carries VersionGroupingMerged; every other value
// says why the items stay apart.
const (
	VersionGroupingMerged          = "merged"
	VersionGroupingHeldLocked      = "locked"
	VersionGroupingHeldManual      = "manual"
	VersionGroupingHeldImported    = "imported"
	VersionGroupingKind            = "kind"
	VersionGroupingIDConflict      = "external_id_conflict"
	VersionGroupingNoSharedID      = "no_shared_external_id"
	VersionGroupingNumbers         = "numbers_differ"
	VersionGroupingDurationUnknown = "duration_unknown"
	VersionGroupingDuration        = "duration_mismatch"
	// VersionGroupingAlone: no other synchronised item shares a strong
	// provider ID.
	VersionGroupingAlone = "alone"
	// VersionGroupingIncompatible: the evidence agreed but the merge
	// boundaries (series, children, conflicting facts) refused.
	VersionGroupingIncompatible = "incompatible"
	// VersionGroupingBusy: an item was played, probed, written or shared;
	// it is judged again on the next run.
	VersionGroupingBusy = "busy"
	// VersionGroupingNFONumbers marks a merge (or a kept-apart new file)
	// where NFO episode numbers overrode the file name (G21.2).
	VersionGroupingNFONumbers = "nfo_numbers_over_filename"
	// VersionGroupingNFONumbersDiffer keeps a new file out of the group its
	// file name points at, because the item's NFO numbers say otherwise.
	VersionGroupingNFONumbersDiffer = "nfo_numbers_differ"
)

// VersionGroupingCandidate is one synchronised Movie or Episode as the
// versions phase sees it.
type VersionGroupingCandidate struct {
	ItemID string
	Kind   string
	// ExternalIDs is the item's NFO uniqueIds document ([{type,value}]).
	ExternalIDs []byte
	// Season, Episode and EpisodeEnd are the effective numbers: a locked
	// correction, else NFO facts, else the file name (G21.2).
	Season, Episode, EpisodeEnd *int
	// NFONumbers reports NFO numbers that differ from the file name's.
	NFONumbers bool
	// Held names why the item may not be changed automatically: an
	// identity lock, a manual version decision or a version that
	// synchronisation does not manage. Empty when it may be.
	Held string
	// Durations are the probed runtimes of every version in microseconds;
	// a version without a usable probe is nil.
	Durations []*int64
}

// VersionGroupingDecision is the outcome for a pair of candidates.
type VersionGroupingDecision struct {
	Merge  bool
	Reason string
	// Evidence names what supported a merge: "external_id:<provider>",
	// "duration" and VersionGroupingNFONumbers when it applied.
	Evidence []string
}

// VersionDurationsClose reports whether two runtimes may belong to versions
// of the same work. Non-positive runtimes are unknown and never close.
func VersionDurationsClose(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	longer, diff := max(a, b), a-b
	if diff < 0 {
		diff = -diff
	}
	tolerance := min(versionDurationCeilingMicros, max(versionDurationFloorMicros, longer/50))
	return diff <= tolerance
}

// SharedVersionGroupingIDs returns the strong providers both documents name
// with a common value.
func SharedVersionGroupingIDs(a, b []byte) []string {
	left, right := externalIDs(a), externalIDs(b)
	var out []string
	for _, provider := range VersionGroupingProviders {
		for v := range left[provider] {
			if right[provider][v] {
				out = append(out, provider)
				break
			}
		}
	}
	return out
}

// VersionGroupingProbes returns single-entry uniqueIds documents, exactly as
// stored, for each strong provider ID of doc. A containment search with them
// finds the items that may share an ID; the decision compares again without
// regard to case.
func VersionGroupingProbes(doc []byte) []string {
	var entries []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if len(doc) == 0 || json.Unmarshal(doc, &entries) != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !slices.Contains(VersionGroupingProviders, strings.ToLower(strings.TrimSpace(e.Type))) || strings.TrimSpace(e.Value) == "" {
			continue
		}
		probe, err := json.Marshal([]map[string]string{{"type": e.Type, "value": e.Value}})
		if err == nil && !slices.Contains(out, string(probe)) {
			out = append(out, string(probe))
		}
	}
	return out
}

// DecideVersionGrouping judges whether two synchronised items are versions
// of the same work. Without enough evidence they stay apart: a shared strong
// provider ID with no conflicting ID, equal effective episode numbers, and
// every version probed with runtimes within tolerance of each other.
func DecideVersionGrouping(a, b VersionGroupingCandidate) VersionGroupingDecision {
	switch {
	case a.Held != "":
		return VersionGroupingDecision{Reason: a.Held}
	case b.Held != "":
		return VersionGroupingDecision{Reason: b.Held}
	case a.Kind != b.Kind || !(a.Kind == "Movie" || a.Kind == "Episode"):
		return VersionGroupingDecision{Reason: VersionGroupingKind}
	case len(ExternalIDConflicts(a.ExternalIDs, b.ExternalIDs)) > 0:
		return VersionGroupingDecision{Reason: VersionGroupingIDConflict}
	}
	shared := SharedVersionGroupingIDs(a.ExternalIDs, b.ExternalIDs)
	if len(shared) == 0 {
		return VersionGroupingDecision{Reason: VersionGroupingNoSharedID}
	}
	if a.Kind == "Episode" && (!sameNumber(a.Season, b.Season) || !sameNumber(a.Episode, b.Episode) || !sameNumber(a.EpisodeEnd, b.EpisodeEnd)) {
		return VersionGroupingDecision{Reason: VersionGroupingNumbers}
	}
	if !knownDurations(a.Durations) || !knownDurations(b.Durations) {
		return VersionGroupingDecision{Reason: VersionGroupingDurationUnknown}
	}
	for _, x := range a.Durations {
		for _, y := range b.Durations {
			if !VersionDurationsClose(*x, *y) {
				return VersionGroupingDecision{Reason: VersionGroupingDuration}
			}
		}
	}
	evidence := make([]string, 0, len(shared)+2)
	for _, provider := range shared {
		evidence = append(evidence, "external_id:"+provider)
	}
	evidence = append(evidence, "duration")
	if a.NFONumbers || b.NFONumbers {
		evidence = append(evidence, VersionGroupingNFONumbers)
	}
	return VersionGroupingDecision{Merge: true, Reason: VersionGroupingMerged, Evidence: evidence}
}

// sameNumber requires both numbers to be known and equal: an episode
// without numbers is never matched.
func sameNumber(a, b *int) bool { return a != nil && b != nil && *a == *b }

func knownDurations(d []*int64) bool {
	if len(d) == 0 {
		return false
	}
	for _, v := range d {
		if v == nil || *v <= 0 {
			return false
		}
	}
	return true
}

// VersionGroupingEvidence digests everything a decision about c depends on.
// The versions phase judges an item again only when this changes: a new NFO
// ID, a new probe, a lock or decision, or a new CatalogGroupingVersion.
func VersionGroupingEvidence(c VersionGroupingCandidate) []byte {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	number := func(v *int) {
		if v == nil {
			field("-")
			return
		}
		field(strconv.Itoa(*v))
	}
	field(CatalogGroupingVersion)
	field(c.Kind)
	field(string(c.ExternalIDs))
	number(c.Season)
	number(c.Episode)
	number(c.EpisodeEnd)
	field(c.Held)
	for _, d := range c.Durations {
		if d == nil {
			field("-")
			continue
		}
		field(strconv.FormatInt(*d, 10))
	}
	return h.Sum(nil)
}

// CatalogNumbersDisagree reports whether an item's NFO episode numbers name
// another episode than a new file's name. NFO wins over the file name
// (G21.2), so the file is kept out of that item. A locked correction is not
// consulted here: it describes the group, and versions of the corrected
// file still join it (G21.5).
func CatalogNumbersDisagree(nfoSeason, nfoEpisode *int, season, episode int) bool {
	return nfoSeason != nil && *nfoSeason != season || nfoEpisode != nil && *nfoEpisode != episode
}
