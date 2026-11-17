package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type sidecarProbeRepository struct {
	pages     [][]domain.SidecarInspectionTarget
	afters    []string
	recorded  [][]domain.SidecarProbeResult
	pageErr   error
	recordErr error
}

func (r *sidecarProbeRepository) NextSidecarProbes(_ context.Context, _ domain.JobLease, after string, limit int) ([]domain.SidecarInspectionTarget, error) {
	if r.pageErr != nil {
		return nil, r.pageErr
	}
	if limit != domain.SidecarProbeBatch {
		return nil, domain.ErrInvalid
	}
	r.afters = append(r.afters, after)
	if index := len(r.afters) - 1; index < len(r.pages) {
		return r.pages[index], nil
	}
	return nil, nil
}

func (r *sidecarProbeRepository) RecordSidecarProbes(_ context.Context, _ domain.JobLease, results []domain.SidecarProbeResult) (int, error) {
	if r.recordErr != nil {
		return 0, r.recordErr
	}
	if len(results) > domain.SidecarProbeBatch {
		return 0, domain.ErrInvalid
	}
	r.recorded = append(r.recorded, results)
	return len(results), nil
}

func (r *sidecarProbeRepository) all() []domain.SidecarProbeResult {
	var out []domain.SidecarProbeResult
	for _, page := range r.recorded {
		out = append(out, page...)
	}
	return out
}

type sidecarProber struct {
	results map[string]error
	meta    map[string]domain.MediaMetadata
	stamp   map[string]int64
	calls   int
	hook    func()
}

func (p *sidecarProber) ProbeSidecar(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error) {
	p.calls++
	if p.hook != nil {
		p.hook()
	}
	key := source.RelativePath
	if err := p.results[key]; err != nil {
		return domain.ProbeObservation{}, err
	}
	size := int64(10)
	if v, ok := p.stamp[key]; ok {
		size = v
	}
	return domain.ProbeObservation{Metadata: p.meta[key], Stamp: domain.ProbeStamp{Size: size, ModifiedUnixNano: 7}}, ctx.Err()
}

func sidecarProbeTarget(key, kind string) domain.SidecarInspectionTarget {
	return domain.SidecarInspectionTarget{ID: fmt.Sprintf("00000000-0000-4000-8000-%012x", []byte(key)[:min(6, len(key))]), Kind: kind, Format: "mka",
		RootPath: "/media", RelativePath: key, Size: 10, ModifiedUnixNano: 7}
}

func sidecarProbeRunner(options *SidecarProbeOptions) *Runner {
	return &Runner{options: Options{DBOperationTimeout: time.Second, PollInterval: time.Millisecond, CatalogSync: &CatalogSyncOptions{SidecarProbes: options}}}
}

// Each probe outcome is either remembered for the stamp that was read or
// left pending for the next sync (B4).
func TestSidecarProbePassOutcomes(t *testing.T) {
	text := func(v string) *string { return &v }
	channels := int64(6)
	audio := domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "audio", Codec: text("dts"), Profile: text("DTS-HD MA"),
		Audio: &domain.MediaAudio{Channels: &channels, ChannelLayout: text("5.1(side)")}}}}
	subtitle := domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "subtitle", Codec: text("hdmv_pgs_subtitle")}}}
	page := []domain.SidecarInspectionTarget{
		sidecarProbeTarget("ready", domain.SidecarKindAudio), sidecarProbeTarget("sup", domain.SidecarKindSubtitle),
		sidecarProbeTarget("nostream", domain.SidecarKindAudio), sidecarProbeTarget("broken", domain.SidecarKindAudio),
		sidecarProbeTarget("garbage", domain.SidecarKindAudio), sidecarProbeTarget("huge", domain.SidecarKindAudio),
		sidecarProbeTarget("busy", domain.SidecarKindAudio), sidecarProbeTarget("slow", domain.SidecarKindAudio),
		sidecarProbeTarget("changed", domain.SidecarKindAudio), sidecarProbeTarget("gone", domain.SidecarKindAudio),
		sidecarProbeTarget("restamped", domain.SidecarKindAudio),
	}
	repository := &sidecarProbeRepository{pages: [][]domain.SidecarInspectionTarget{page}}
	prober := &sidecarProber{
		meta: map[string]domain.MediaMetadata{"ready": audio, "sup": subtitle, "nostream": subtitle, "restamped": audio},
		results: map[string]error{"broken": domain.ErrProbeFailed, "garbage": domain.ErrProbeMetadataInvalid, "huge": domain.ErrProbeOutputLimit,
			"busy": domain.ErrProbeBusy, "slow": context.DeadlineExceeded, "changed": domain.ErrProbeSourceChanged, "gone": domain.ErrProbeInputUnavailable},
		stamp: map[string]int64{"restamped": 11},
	}
	if storage, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: prober}).probeSidecars(context.Background(), domain.JobLease{}); err != nil || storage {
		t.Fatal(err, storage)
	}
	got := map[string]domain.SidecarProbeResult{}
	for _, result := range repository.all() {
		got[result.ID] = result
	}
	if len(got) != 6 || prober.calls != len(page) {
		t.Fatalf("recorded %d of %d probes", len(got), prober.calls)
	}
	ready := got[page[0].ID]
	if ready.State != domain.SidecarProbeReady || ready.Facts != (domain.SidecarProbeFacts{Codec: "dts", Profile: "DTS-HD MA", Channels: 6, ChannelLayout: "5.1(side)"}) ||
		ready.Size != 10 || ready.ModifiedUnixNano != 7 {
		t.Fatalf("ready: %+v", ready)
	}
	if sup := got[page[1].ID]; sup.State != domain.SidecarProbeReady || sup.Facts.Codec != "hdmv_pgs_subtitle" {
		t.Fatalf("sup: %+v", sup)
	}
	for _, key := range []int{2, 3, 4, 5} {
		if result := got[page[key].ID]; result.State != domain.SidecarProbeFailed || result.Facts != (domain.SidecarProbeFacts{}) {
			t.Fatalf("%s must be remembered as failed: %+v", page[key].RelativePath, result)
		}
	}
	for _, key := range []int{6, 7, 8, 9, 10} {
		if _, recorded := got[page[key].ID]; recorded {
			t.Fatalf("%s is transient and must stay pending", page[key].RelativePath)
		}
	}
}

func TestSidecarProbePassStopsAndBounds(t *testing.T) {
	lease := domain.JobLease{}
	if storage, err := sidecarProbeRunner(nil).probeSidecars(context.Background(), lease); err != nil || storage {
		t.Fatal("disabled pass")
	}
	// An unavailable runtime ends the pass but keeps what was already probed.
	page := []domain.SidecarInspectionTarget{sidecarProbeTarget("a", domain.SidecarKindAudio), sidecarProbeTarget("b", domain.SidecarKindAudio), sidecarProbeTarget("c", domain.SidecarKindAudio)}
	repository := &sidecarProbeRepository{pages: [][]domain.SidecarInspectionTarget{page}}
	prober := &sidecarProber{results: map[string]error{"b": domain.ErrProbeRuntimeUnavailable}}
	if _, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: prober}).probeSidecars(context.Background(), lease); err != nil || prober.calls != 2 || len(repository.all()) != 1 {
		t.Fatal("unavailable runtime did not stop the pass", err, prober.calls)
	}
	off := &sidecarProber{}
	if _, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: off, Available: func() bool { return false }}).probeSidecars(context.Background(), lease); err != nil || off.calls != 0 {
		t.Fatal("unavailable capability ran probes")
	}
	sentinel := errors.New("lease lost")
	for _, repository := range []*sidecarProbeRepository{{pageErr: sentinel}, {pages: [][]domain.SidecarInspectionTarget{page}, recordErr: sentinel}} {
		if storage, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: &sidecarProber{}}).probeSidecars(context.Background(), lease); !errors.Is(err, sentinel) || !storage {
			t.Fatal("repository error", err, storage)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	repository = &sidecarProbeRepository{pages: [][]domain.SidecarInspectionTarget{page}}
	if storage, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: &sidecarProber{hook: cancel}}).probeSidecars(ctx, lease); !errors.Is(err, context.Canceled) || storage || len(repository.recorded) != 0 {
		t.Fatal("cancellation", err)
	}
	// Full pages continue after the last ID; at most SidecarProbeMaxPerJob
	// probes start in one job.
	var pages [][]domain.SidecarInspectionTarget
	for i := 0; i*domain.SidecarProbeBatch <= domain.SidecarProbeMaxPerJob; i++ {
		page := make([]domain.SidecarInspectionTarget, domain.SidecarProbeBatch)
		for j := range page {
			page[j] = sidecarProbeTarget(fmt.Sprintf("%d-%d", i, j), domain.SidecarKindAudio)
		}
		pages = append(pages, page)
	}
	counting := &sidecarProber{}
	repository = &sidecarProbeRepository{pages: pages}
	if _, err := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: counting, FileTimeout: time.Hour}).probeSidecars(context.Background(), lease); err != nil ||
		counting.calls != domain.SidecarProbeMaxPerJob || repository.afters[1] != pages[0][domain.SidecarProbeBatch-1].ID {
		t.Fatal("per-job bound or paging", err, counting.calls)
	}
}

// The probe pass runs after the inspection pass once the batches are done.
func TestCatalogSyncRunsTheSidecarProbePass(t *testing.T) {
	sync := &coverSyncRepository{}
	repository := &sidecarProbeRepository{pages: [][]domain.SidecarInspectionTarget{{sidecarProbeTarget("a", domain.SidecarKindAudio)}}}
	r := sidecarProbeRunner(&SidecarProbeOptions{Repository: repository, Prober: &sidecarProber{}})
	r.options.CatalogSync.Repository = sync
	if err, storage := r.executeCatalogSync(context.Background(), domain.JobLease{}); err != nil || storage || len(repository.all()) != 1 {
		t.Fatal("catalog sync did not run the sidecar probe pass", err)
	}
}
