package domain

import (
	"errors"
	"time"
)

// Playback sessions and watch progress (G23.1, G23.2, G23.4, G20.4).
//
// A playback session is one client playing one logical item: it is keyed by
// the user and a play key the client reports (the third-party play session
// identifier, or one derived from the authenticated session and the item),
// so repeated start reports of the same play key are the same session.
// Progress belongs to the logical item, never to a file: every version of an
// item shares one resume point and one played state per user. The source
// that was playing is kept on the session for the statistics of G23.3.

// ErrPlaybackBusy reports that the in-memory progress buffer reached its
// configured session bound; the client should retry later.
var ErrPlaybackBusy = errors.New("playback progress buffer full")

// PlaybackTicksPerSecond is the position unit shared with third-party
// clients: 100-nanosecond ticks.
const PlaybackTicksPerSecond = 10_000_000

const (
	// PlayKeyMax bounds a client supplied play key in bytes.
	PlayKeyMax = 128
	// PlaybackPositionMax bounds a reported position: 100 days in ticks.
	PlaybackPositionMax = 100 * 24 * 3600 * PlaybackTicksPerSecond
	// PlaybackDeliveryDirect is the only delivery method: the original file.
	PlaybackDeliveryDirect = "direct"
)

// PlaybackState is the lifecycle state of a playback session.
type PlaybackState string

const (
	PlaybackActive   PlaybackState = "active"
	PlaybackStopped  PlaybackState = "stopped"
	PlaybackFailed   PlaybackState = "failed"
	PlaybackTimedOut PlaybackState = "timed_out"
)

// Failure reason codes of a failed session (G23.1). PlaybackFailureError is
// the reason recorded when a client reports a failure without one.
const (
	PlaybackFailureTranscodeDisabled = "transcode_disabled"
	PlaybackFailureCodecUnsupported  = "codec_unsupported"
	PlaybackFailureClientBlocked     = "client_blocked"
	PlaybackFailurePermissionDenied  = "permission_denied"
	PlaybackFailureError             = "playback_error"
)

// PlaybackFailureReasons lists every accepted failure reason.
var PlaybackFailureReasons = []string{PlaybackFailureTranscodeDisabled, PlaybackFailureCodecUnsupported, PlaybackFailureClientBlocked, PlaybackFailurePermissionDenied, PlaybackFailureError}

func ValidPlaybackFailureReason(reason string) bool {
	for _, r := range PlaybackFailureReasons {
		if r == reason {
			return true
		}
	}
	return false
}

// PlaybackReportKind is the kind of one client report.
type PlaybackReportKind string

const (
	PlaybackReportStart    PlaybackReportKind = "start"
	PlaybackReportProgress PlaybackReportKind = "progress"
	PlaybackReportStop     PlaybackReportKind = "stop"
	// PlaybackReportPing only keeps a session alive.
	PlaybackReportPing PlaybackReportKind = "ping"
)

// PlaybackReport is one report from a client. ItemID may be empty only for a
// ping or for a report of a play key the server already knows.
type PlaybackReport struct {
	Kind    PlaybackReportKind
	PlayKey string
	ItemID  string
	// SourceID optionally names the version being played; it must belong to
	// the item.
	SourceID string
	// PositionTicks is valid when PositionKnown is set.
	PositionTicks int64
	PositionKnown bool
	Paused        bool
	// Failed marks a stop report of a failed playback; FailureReason is one
	// of PlaybackFailureReasons or empty.
	Failed        bool
	FailureReason string
}

// ValidPlayKey accepts 1 to PlayKeyMax bytes of letters, digits and ._:-.
func ValidPlayKey(key string) bool {
	if key == "" || len(key) > PlayKeyMax {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// DerivedPlayKey is the play key of a client that sent none: one session per
// authenticated session and item, like upstream's per-device now playing.
func DerivedPlayKey(sessionID, itemID string) string {
	return "s:" + sessionID + ":" + itemID
}

// Validate checks a report's shape before it reaches the buffer.
func (r PlaybackReport) Validate() error {
	switch r.Kind {
	case PlaybackReportStart, PlaybackReportProgress, PlaybackReportStop, PlaybackReportPing:
	default:
		return ErrInvalid
	}
	if !ValidPlayKey(r.PlayKey) {
		return ErrInvalid
	}
	if r.ItemID != "" && !ValidID(r.ItemID) || r.SourceID != "" && !ValidID(r.SourceID) {
		return ErrInvalid
	}
	if r.PositionKnown && (r.PositionTicks < 0 || r.PositionTicks > PlaybackPositionMax) {
		return ErrInvalid
	}
	if r.FailureReason != "" && (!r.Failed || !ValidPlaybackFailureReason(r.FailureReason)) {
		return ErrInvalid
	}
	if r.Failed && r.Kind != PlaybackReportStop {
		return ErrInvalid
	}
	return nil
}

// UserItemData is one user's progress on one logical item (G20.4).
type UserItemData struct {
	ItemID string `json:"itemId"`
	// ResumeTicks is the resume point; zero means start from the beginning.
	ResumeTicks  int64      `json:"resumeTicks"`
	Played       bool       `json:"played"`
	PlayCount    int        `json:"playCount"`
	LastPlayedAt *time.Time `json:"lastPlayedAt,omitempty"`
}

// ProgressResume is the resume point recorded while a session plays:
// positions before MinResumePosition or past the completion share of a known
// runtime leave none, so the start or the credits never become a resume
// point (the same thresholds as ComputeWatchSession).
func ProgressResume(position, runtime int64, rules WatchStatsRules) int64 {
	if position < DurationToTicks(rules.MinResumePosition) || runtime > 0 && float64(position) >= float64(runtime)*rules.CompletedRatio {
		return 0
	}
	return position
}

// ResolvePlaybackEnd decides the outcome of a session that ended at
// position: completed sessions (position at or past CompletedRatio of a
// known runtime) mark the item played, count one play and clear the resume
// point; any other end keeps ProgressResume as the resume point. With an
// unknown runtime nothing is ever completed by position alone.
func ResolvePlaybackEnd(position, runtime int64, rules WatchStatsRules) (resume int64, completed bool) {
	if runtime > 0 && float64(position) >= float64(runtime)*rules.CompletedRatio {
		return 0, true
	}
	return ProgressResume(position, runtime, rules), false
}

// TicksToDuration converts ticks to a duration.
func TicksToDuration(ticks int64) time.Duration { return time.Duration(ticks) * 100 }

// DurationToTicks converts a duration to ticks.
func DurationToTicks(d time.Duration) int64 { return int64(d / 100) }

// PlaybackSample is one stored sample of a session's report stream, the
// input of ComputeWatchSession. Seq orders the samples of one session.
type PlaybackSample struct {
	Seq           int
	At            time.Time
	Kind          WatchSampleKind
	PositionTicks int64
	Paused        bool
}

// WatchSample converts a stored sample for the statistics rules.
func (s PlaybackSample) WatchSample() WatchSample {
	return WatchSample{At: s.At, Kind: s.Kind, Position: TicksToDuration(s.PositionTicks), Paused: s.Paused}
}

// PlaybackStart is what storage needs to open (or rejoin) a session.
type PlaybackStart struct {
	Actor    Actor
	PlayKey  string
	ItemID   string
	SourceID string
	// RuntimeTicks is the chosen source's duration; zero when unknown.
	RuntimeTicks  int64
	At            time.Time
	PositionTicks int64
	// PositionKnown lets a rejoined session take PositionTicks; otherwise it
	// keeps its stored position.
	PositionKnown bool
	Paused        bool
}

// PlaybackSessionRecord is a stored session as storage returns it.
type PlaybackSessionRecord struct {
	ID string
	// Created is set when StartPlayback inserted the session rather than
	// rejoining a stored one.
	Created       bool
	UserID        string
	ItemID        string
	SourceID      string
	StartedAt     time.Time
	LastReportAt  time.Time
	PositionTicks int64
	RuntimeTicks  int64
	Paused        bool
	SampleCount   int
}

// PlaybackEnding closes a session in a flush.
type PlaybackEnding struct {
	State         PlaybackState
	At            time.Time
	FailureReason string
}

// PlaybackFlush is the latest buffered state of one session, written in one
// batch with every other dirty session. Only an active stored session is
// updated; an ended, cleared or deleted one is left alone, and so is the
// user data it would have touched.
type PlaybackFlush struct {
	SessionID     string
	PositionTicks int64
	Paused        bool
	LastReportAt  time.Time
	// Reports counts the client reports coalesced into this flush.
	Reports int
	// ResumeTicks is the resume point to store for the item.
	ResumeTicks int64
	// Completed marks the item played and counts one play.
	Completed bool
	// SourceID is the version last played, recorded with the user data.
	SourceID string
	End      *PlaybackEnding
	Samples  []PlaybackSample
}

// PlaybackFlushResult counts what one flush wrote.
type PlaybackFlushResult struct {
	Statements int
	Sessions   int
	UserData   int
	Samples    int
}

// ResumeQuery pages the continue watching list.
type ResumeQuery struct {
	Offset int
	Limit  int
	// Kinds restricts the item kinds; empty means every playable kind.
	Kinds []string
}

// ResumeEntry is one item to continue with its user data.
type ResumeEntry struct {
	Item BrowseItem
	Data UserItemData
	// RuntimeTicks is the runtime of the version last played; zero when
	// unknown.
	RuntimeTicks int64
}

type ResumePage struct {
	Items []ResumeEntry
	Total int
}

// ValidResumeQuery checks a continue watching request.
func ValidResumeQuery(q ResumeQuery) bool {
	if q.Offset < 0 || q.Offset > BrowseOffsetMax || q.Limit < 1 || q.Limit > BrowseLimitMax || len(q.Kinds) > len(browseKinds) {
		return false
	}
	for _, kind := range q.Kinds {
		if !browseKinds[kind] {
			return false
		}
	}
	return true
}

// ActivePlayback is one running session as administrators see it. Device
// and client labels are client-supplied, never proofs.
type ActivePlayback struct {
	ID            string    `json:"id"`
	UserID        string    `json:"userId"`
	UserName      string    `json:"userName"`
	DeviceID      string    `json:"deviceId,omitempty"`
	ClientName    string    `json:"clientName,omitempty"`
	ItemID        string    `json:"itemId"`
	ItemTitle     string    `json:"itemTitle"`
	SourceID      string    `json:"sourceId,omitempty"`
	Delivery      string    `json:"delivery"`
	StartedAt     time.Time `json:"startedAt"`
	LastReportAt  time.Time `json:"lastReportAt"`
	PositionTicks int64     `json:"positionTicks"`
	RuntimeTicks  int64     `json:"runtimeTicks,omitempty"`
	Paused        bool      `json:"paused"`
}

// ActivePlaybackMax bounds the administrator listing.
const ActivePlaybackMax = 500
