package calendar

import (
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// DailyWindow is an immutable allowed local-time interval. A nil window allows
// all times. Start is inclusive and end exclusive, including across midnight.
type DailyWindow struct {
	zone       *time.Location
	start, end int
}

// ParseDailyWindow requires all three fields or none. Equal endpoints are
// rejected: unrestricted operation is represented by the empty configuration.
func ParseDailyWindow(start, end, timezone string) (*DailyWindow, error) {
	if start == "" && end == "" && timezone == "" {
		return nil, nil
	}
	minute := func(s string) (int, bool) {
		if len(s) != 5 || s[2] != ':' || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' || s[3] < '0' || s[3] > '9' || s[4] < '0' || s[4] > '9' {
			return 0, false
		}
		h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
		return h*60 + m, h < 24 && m < 60
	}
	a, okA := minute(start)
	b, okB := minute(end)
	if !okA || !okB || a == b || len(timezone) < 1 || len(timezone) > 128 || timezone == "Local" || strings.ContainsAny(timezone, " \t\r\n\\") {
		return nil, domain.ErrInvalid
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, domain.ErrInvalid
	}
	return &DailyWindow{zone: zone, start: a, end: b}, nil
}

// Allows uses the instant's actual UTC offset. Both occurrences of a repeated
// DST minute follow the same wall-clock rule; nonexistent minutes never match.
// It does not construct ambiguous local dates or assume days last 24 hours.
func (w *DailyWindow) Allows(at time.Time) bool {
	if w == nil {
		return true
	}
	local := at.In(w.zone)
	minute := local.Hour()*60 + local.Minute()
	if w.start < w.end {
		return minute >= w.start && minute < w.end
	}
	return minute >= w.start || minute < w.end
}
