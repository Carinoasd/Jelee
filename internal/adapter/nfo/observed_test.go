package nfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type observedReaderFake struct {
	read func(context.Context, domain.NFOSource) (app.NFOReadSource, error)
}

func (*observedReaderFake) Identity() domain.NFOIdentity { return domain.DefaultNFOIdentity() }
func (f *observedReaderFake) Read(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
	return f.read(c, s)
}

type observedSourceFake struct {
	stamp domain.NFOStamp
	parse func(context.Context) (domain.NFOValidationSummary, error)
}

func (s *observedSourceFake) Stamp() domain.NFOStamp { return s.stamp }
func (s *observedSourceFake) Parse(c context.Context) (domain.NFOValidationSummary, error) {
	return s.parse(c)
}

func TestObservedReaderCountsActualCallsAndRedactsRetainedSources(t *testing.T) {
	root := t.TempDir()
	raw := []byte("<movie><title>PRIVATE_NFO_TITLE</title></movie>")
	if err := os.WriteFile(filepath.Join(root, "one.nfo"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	base, _ := NewSummaryReader(DefaultMaxBytes)
	r, err := NewObservedReader(base)
	if err != nil {
		t.Fatal(err)
	}
	source, err := r.Read(context.Background(), domain.NFOSource{RootPath: root, RelativePath: "one.nfo"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := source.Parse(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	final, err := r.Read(context.Background(), domain.NFOSource{RootPath: root, RelativePath: "one.nfo"})
	if err != nil || source.Stamp() != final.Stamp() {
		t.Fatal("repeat source changed")
	}
	want := ReaderStats{ReadCalls: 2, CompletedReads: 2, CompletedReadBytes: uint64(2 * len(raw)), HashCompletions: 2, ParseCalls: 2, PeakCalls: 1}
	if r.Stats() != want {
		t.Fatalf("actual invocations: %+v", r.Stats())
	}
	for _, v := range []any{r, source, final} {
		encoded, _ := json.Marshal(v)
		for _, text := range []string{string(encoded), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v)} {
			if strings.Contains(text, "PRIVATE_NFO_TITLE") || strings.Contains(text, root) || strings.Contains(text, source.Stamp().SHA256) {
				t.Fatal("statistics wrapper leaked retained data")
			}
		}
	}
}

func TestObservedReaderFailureAccountingAndPanicUnwind(t *testing.T) {
	if r, e := NewObservedReader(nil); r != nil || e != domain.ErrInvalid {
		t.Fatal("nil reader accepted")
	}
	var nilReader *ObservedReader
	if nilReader.Stats() != (ReaderStats{}) || nilReader.Identity() != (domain.NFOIdentity{}) {
		t.Fatal("nil observer exposes values")
	}
	if s, e := nilReader.Read(context.Background(), domain.NFOSource{}); s != nil || e != domain.ErrNFOReaderUnavailable {
		t.Fatal("nil observer read")
	}
	var nilSource *observedSource
	if nilSource.Stamp() != (domain.NFOStamp{}) {
		t.Fatal("nil source stamp")
	}
	if _, e := nilSource.Parse(context.Background()); e != domain.ErrNFOReaderUnavailable {
		t.Fatal("nil source parse")
	}
	stamp := domain.NFOStamp{Size: 7, ModifiedUnixNano: 1, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}
	fake := &observedReaderFake{read: func(context.Context, domain.NFOSource) (app.NFOReadSource, error) {
		return &observedSourceFake{stamp: stamp}, domain.ErrNFOInputUnavailable
	}}
	r, _ := NewObservedReader(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if source, e := r.Read(c, domain.NFOSource{}); source != nil || e == nil {
			t.Fatal("invalid context delegated")
		}
	}
	if r.Stats().ReadCalls != 0 {
		t.Fatal("counted a call never delegated")
	}
	if source, e := r.Read(context.Background(), domain.NFOSource{}); source != nil || e != domain.ErrNFOInputUnavailable {
		t.Fatal("failed source escaped")
	}
	if r.Stats().ReadCalls != 1 || r.Stats().CompletedReadBytes != 0 || r.Stats().CompletedReads != 0 {
		t.Fatal("invented partial read bytes")
	}
	fake.read = func(context.Context, domain.NFOSource) (app.NFOReadSource, error) { panic("synthetic-private") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("observer swallowed backend panic")
			}
		}()
		_, _ = r.Read(context.Background(), domain.NFOSource{})
	}()
	if r.Stats().ActiveCalls != 0 {
		t.Fatal("panicked read leaked active count")
	}
	for _, source := range []app.NFOReadSource{nil, &observedSourceFake{}} {
		fake.read = func(context.Context, domain.NFOSource) (app.NFOReadSource, error) { return source, nil }
		if s, e := r.Read(context.Background(), domain.NFOSource{}); s != nil || e != domain.ErrNFOReaderUnavailable {
			t.Fatal("invalid successful observation accepted")
		}
	}
	fake.read = func(context.Context, domain.NFOSource) (app.NFOReadSource, error) {
		return &observedSourceFake{stamp: stamp, parse: func(context.Context) (domain.NFOValidationSummary, error) {
			return domain.NFOValidationSummary{Status: "invalid"}, context.DeadlineExceeded
		}}, nil
	}
	source, e := r.Read(context.Background(), domain.NFOSource{})
	if e != nil {
		t.Fatal(e)
	}
	if v, e := source.Parse(context.Background()); !errors.Is(e, context.DeadlineExceeded) || v.Status != "" {
		t.Fatal("failed parse retained summary")
	}
	if _, e = source.Parse(nil); e != domain.ErrInvalid {
		t.Fatal("nil parse context")
	}
	if r.Stats().ParseCalls != 1 || r.Stats().ActiveCalls != 0 || r.Stats().CompletedReadBytes != 7 {
		t.Fatal("parse/failure accounting")
	}
}

func TestObservedReaderConcurrentCallsJoinAndCountPeak(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &observedReaderFake{read: func(c context.Context, _ domain.NFOSource) (app.NFOReadSource, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return nil, domain.ErrNFOInputUnavailable
		case <-c.Done():
			return nil, c.Err()
		}
	}}
	r, _ := NewObservedReader(fake)
	results := make(chan error, 2)
	for range 2 {
		go func() { _, e := r.Read(ctx, domain.NFOSource{}); results <- e }()
	}
	defer func() {
		cancel()
		for r.Stats().ActiveCalls != 0 {
			select {
			case <-results:
			case <-time.After(time.Second):
				t.Error("observer call did not join")
				return
			}
		}
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("concurrent read not entered")
		}
	}
	if s := r.Stats(); s.ActiveCalls != 2 || s.PeakCalls != 2 || s.ReadCalls != 2 {
		t.Fatal("concurrent calls not represented")
	}
	close(release)
	for range 2 {
		if e := <-results; e != domain.ErrNFOInputUnavailable {
			t.Fatal("backend error changed")
		}
	}
	if s := r.Stats(); s.ActiveCalls != 0 || s.PeakCalls != 2 || s.CompletedReads != 0 {
		t.Fatal("concurrent failure accounting")
	}
}
