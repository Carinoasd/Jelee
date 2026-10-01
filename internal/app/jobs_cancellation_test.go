package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type cancellationNotifierFunc func(string)

func (f cancellationNotifierFunc) NotifyJobCancellation(id string) { f(id) }

func TestJobsCancellationSignalFollowsAuthorizedCommit(t *testing.T) {
	id := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, tc := range []struct {
		name, state string
		requested   bool
		failure     error
		notify      bool
	}{
		{"running committed", domain.JobRunning, true, nil, true},
		{"not requested", domain.JobRunning, false, nil, false},
		{"queued cancellation", domain.JobCancelled, true, nil, false},
		{"already succeeded", domain.JobSucceeded, false, nil, false},
		{"authorization denied", domain.JobRunning, true, domain.ErrForbidden, false},
		{"transaction failed", domain.JobRunning, true, domain.ErrDatabase, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			returned, notifications := false, 0
			service := newJobService(t, jobRepositoryFake{cancel: func(context.Context, domain.Actor, string) (domain.Job, error) {
				returned = true
				return domain.Job{ID: id, State: tc.state, CancelRequested: tc.requested}, tc.failure
			}})
			service.cancellationNotifier = cancellationNotifierFunc(func(got string) {
				if !returned || tc.failure != nil || got != id {
					t.Fatal("notification preceded authorized commit")
				}
				notifications++
			})
			_, err := service.Cancel(context.Background(), accountTestActor(), id)
			if !errors.Is(err, tc.failure) || (notifications == 1) != tc.notify || notifications > 1 {
				t.Fatal("cancellation notification contract", err, notifications)
			}
		})
	}
}
