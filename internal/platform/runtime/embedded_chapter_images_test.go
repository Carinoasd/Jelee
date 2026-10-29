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

type chapterRepositoryStub struct{ app.ChapterImageRepository }
type chapterExtractorStub struct{ app.ChapterImageExtractor }

func TestPrepareChapterImagesIsOptionalAndNeverFailsStartup(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	available := &probeService{capability: domain.ProbeCapability{Enabled: true, Available: true, State: "available"}}
	store := &imageadapter.Store{}
	calls, closed := 0, 0
	prepare := func(context.Context) (app.ChapterImageExtractor, func() error, error) {
		calls++
		return chapterExtractorStub{}, func() error { closed++; return nil }, nil
	}
	if options, closer := prepareChapterImages(context.Background(), false, available, store, chapterRepositoryStub{}, logger, prepare); options != nil || closer != nil || calls != 0 {
		t.Fatal("disabled configuration ran a factory")
	}
	unavailable := &probeService{capability: domain.ProbeCapability{State: "disabled"}}
	for name, probing := range map[string]*probeService{"probe_off": unavailable, "probe_nil": nil} {
		if options, closer := prepareChapterImages(context.Background(), true, probing, store, chapterRepositoryStub{}, logger, prepare); options != nil || closer != nil || calls != 0 {
			t.Fatal("pass enabled without probe:", name)
		}
	}
	if options, _ := prepareChapterImages(context.Background(), true, available, nil, chapterRepositoryStub{}, logger, prepare); options != nil || calls != 0 {
		t.Fatal("pass enabled without an image store")
	}
	if !strings.Contains(logs.String(), "chapter_image_prerequisite_unavailable") || !strings.Contains(logs.String(), "never ffmpeg") {
		t.Fatal("unavailable pass not logged")
	}
	failing := func(context.Context) (app.ChapterImageExtractor, func() error, error) {
		return nil, func() error { closed++; return nil }, errors.New("sandbox")
	}
	if options, closer := prepareChapterImages(context.Background(), true, available, store, chapterRepositoryStub{}, logger, failing); options != nil || closer != nil || closed != 1 ||
		!strings.Contains(logs.String(), "chapter_image_runtime_unavailable") {
		t.Fatal("failed registration left the pass on or leaked scratch")
	}
	options, closer := prepareChapterImages(context.Background(), true, available, store, chapterRepositoryStub{}, logger, prepare)
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
	// The production factory never registers on a host without the shipped,
	// protected runtime.
	if extractor, cleanup, err := prepareProductionChapterExtractor(context.Background()); err == nil || extractor != nil {
		t.Fatal("unprotected host registered chapter reads")
	} else if cleanup != nil {
		_ = cleanup()
	}
}
