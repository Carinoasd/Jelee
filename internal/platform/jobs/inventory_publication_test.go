package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type snapshotPreparationFake struct {
	executionFake
	calls   int
	prepare func(context.Context) (bool, error)
}

func (f *snapshotPreparationFake) PrepareInventoryPublication(ctx context.Context, _ domain.JobLease) (bool, error) {
	f.calls++
	return f.prepare(ctx)
}

func TestIgnorePreparationPrecedesSourceVerification(t *testing.T) {
	for _, family := range []bool{false, true} {
		for _, mode := range []string{"success", "failure", "unknown"} {
			t.Run(map[bool]string{true: "family/", false: "custom/"}[family]+mode, func(t *testing.T) {
				custom := &ignoreRunFake{progress: domain.IgnoreExecutionProgress{ComparisonStarted: true, Unknown: mode == "unknown"}}
				legacy := &familyRunFake{unknown: mode == "unknown"}
				preparer := &snapshotPreparationFake{}
				sentinel := errors.New("preparation failed")
				preparer.prepare = func(ctx context.Context) (bool, error) {
					if _, ok := ctx.Deadline(); !ok || custom.verification != 0 || legacy.begun != 0 || custom.sealed != 0 || legacy.sealed != 0 {
						t.Fatal("snapshot work started after source verification or without DB deadline")
					}
					if mode == "failure" {
						return false, sentinel
					}
					return preparer.calls == 3, nil
				}
				r := &Runner{repository: preparer, options: Options{DBOperationTimeout: time.Second, Ignore: &IgnoreOptions{Repository: custom}, FamilyIgnore: &FamilyIgnoreOptions{Repository: legacy, Scanner: &familyScannerStub{}}}}
				var err error
				if family {
					err, _ = r.executeFamilyIgnore(context.Background(), domain.JobLease{}, domain.IgnoreRequest{})
				} else {
					err, _ = r.executeIgnore(context.Background(), domain.JobLease{}, domain.IgnoreRequest{})
				}
				wantCalls, wantSeals := 3, 1
				if mode == "unknown" {
					wantCalls, wantSeals = 0, 0
				}
				if mode == "failure" {
					wantCalls, wantSeals = 1, 0
					if !errors.Is(err, sentinel) {
						t.Fatal("lost preparation failure", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if preparer.calls != wantCalls || custom.sealed+legacy.sealed != wantSeals {
					t.Fatal("unexpected preparation/verification order", preparer.calls, custom.sealed, legacy.sealed)
				}
			})
		}
	}
}
