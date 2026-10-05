package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type coverRepositoryStub struct{ app.EmbeddedCoverRepository }
type coverExtractorStub struct{ app.EmbeddedCoverExtractor }

func TestPrepareEmbeddedCoversIsOptionalAndNeverFailsStartup(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	available := &probeService{capability: domain.ProbeCapability{Enabled: true, Available: true, State: "available"}}
	store := &imageadapter.Store{}
	calls, closed := 0, 0
	prepare := func(context.Context) (app.EmbeddedCoverExtractor, func() error, error) {
		calls++
		return coverExtractorStub{}, func() error { closed++; return nil }, nil
	}
	if options, closer := prepareEmbeddedCovers(context.Background(), false, available, store, coverRepositoryStub{}, logger, prepare); options != nil || closer != nil || calls != 0 {
		t.Fatal("disabled configuration ran a factory")
	}
	unavailable := &probeService{capability: domain.ProbeCapability{State: "disabled"}}
	for name, probing := range map[string]*probeService{"probe_off": unavailable, "probe_nil": nil} {
		if options, closer := prepareEmbeddedCovers(context.Background(), true, probing, store, coverRepositoryStub{}, logger, prepare); options != nil || closer != nil || calls != 0 {
			t.Fatal("pass enabled without probe:", name)
		}
	}
	if options, _ := prepareEmbeddedCovers(context.Background(), true, available, nil, coverRepositoryStub{}, logger, prepare); options != nil || calls != 0 {
		t.Fatal("pass enabled without an image store")
	}
	if !strings.Contains(logs.String(), "embedded_cover_prerequisite_unavailable") {
		t.Fatal("unavailable pass not logged")
	}
	failing := func(context.Context) (app.EmbeddedCoverExtractor, func() error, error) {
		return nil, func() error { closed++; return nil }, errors.New("sandbox")
	}
	if options, closer := prepareEmbeddedCovers(context.Background(), true, available, store, coverRepositoryStub{}, logger, failing); options != nil || closer != nil || closed != 1 {
		t.Fatal("failed registration left the pass on or leaked scratch")
	}
	options, closer := prepareEmbeddedCovers(context.Background(), true, available, store, coverRepositoryStub{}, logger, prepare)
	if options == nil || closer == nil || calls != 1 || options.Store != store || options.Available == nil || !options.Available() {
		t.Fatal("available pass not wired")
	}
	if err := closer(); err != nil || closed != 2 {
		t.Fatal("scratch cleanup")
	}
	available.Disable()
	if options.Available() {
		t.Fatal("pass ignores a probe capability that turned off")
	}
}
