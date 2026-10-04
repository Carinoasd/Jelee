package domain

// Upper bounds of a direct delivery limit, shared by server configuration and
// per-user overrides so both are validated against the same range.
const (
	MaxDeliveryStreams = 128
	MaxDeliveryKbps    = 10_000_000
)

// DeliveryLimits are per-user overrides of the server-wide direct delivery
// limits (G07.4). A nil field follows the server-wide value; zero exempts the
// user from that limit. Overrides apply only while the matching limit is
// enabled in configuration (G45.4).
type DeliveryLimits struct {
	MaxStreams *int   `json:"maxStreams,omitempty"`
	MaxKbps    *int64 `json:"maxKbps,omitempty"`
}

func (l DeliveryLimits) Valid() bool {
	return (l.MaxStreams == nil || *l.MaxStreams >= 0 && *l.MaxStreams <= MaxDeliveryStreams) &&
		(l.MaxKbps == nil || *l.MaxKbps >= 0 && *l.MaxKbps <= MaxDeliveryKbps)
}

// Equal compares override values rather than pointer identity.
func (l DeliveryLimits) Equal(other DeliveryLimits) bool {
	sameStreams := l.MaxStreams == nil && other.MaxStreams == nil || l.MaxStreams != nil && other.MaxStreams != nil && *l.MaxStreams == *other.MaxStreams
	sameKbps := l.MaxKbps == nil && other.MaxKbps == nil || l.MaxKbps != nil && other.MaxKbps != nil && *l.MaxKbps == *other.MaxKbps
	return sameStreams && sameKbps
}
