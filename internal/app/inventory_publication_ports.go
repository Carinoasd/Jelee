package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// InventoryPublicationPreparer stages at most one bounded batch per call.
// The worker keeps its normal cancellation and heartbeat monitor running until
// preparation finishes. Only FinishJob makes the prepared snapshot visible.
type InventoryPublicationPreparer interface {
	PrepareInventoryPublication(context.Context, domain.JobLease) (bool, error)
}
