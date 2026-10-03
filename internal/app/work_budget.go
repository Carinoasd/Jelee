package app

import "context"

// WorkClass describes the dominant resource of one non-nested operation.
type WorkClass uint8

const (
	WorkCPU WorkClass = iota + 1
	WorkIO
)

// WorkBudget admits a single operation. A successful caller must invoke release
// after all child activity joins, including on cancellation or error. Callers
// must release before acquiring another class; never hold nested permits.
type WorkBudget interface {
	Acquire(context.Context, WorkClass) (release func(), err error)
}
