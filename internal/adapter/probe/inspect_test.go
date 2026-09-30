package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func assertEmptyStamp(t *testing.T, stamp domain.ProbeStamp, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || err.Error() != want.Error() || stamp != (domain.ProbeStamp{}) {
		t.Fatalf("failure returned stamp or unsafe error: error=%v", err)
	}
}

func TestInspectReadOnlyStableAndDoesNotNeedProcess(t *testing.T) {
	source, name, data := adapterFixture(t)
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := Inspect(context.Background(), source)
	if err != nil || domain.ValidateProbeStamp(stamp) != nil {
		t.Fatal("valid file inspect failed", err)
	}
	if stamp.Size != int64(len(data)) || stamp.ModifiedUnixNano != before.ModTime().UnixNano() || stamp.FingerprintVersion != domain.ProbeFingerprintVersion {
		t.Fatal("incorrect stamp")
	}
	adapter := &Adapter{}
	again, err := adapter.Inspect(context.Background(), source)
	if err != nil || again != stamp {
		t.Fatal("Inspect incorrectly depends on tool runner")
	}
	input, err := Open(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.Stdin().Seek(17, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := inspectOpened(context.Background(), source, input)
	if err != nil || got != stamp {
		t.Fatal("open FD observation differs")
	}
	if offset, err := input.Stdin().Seek(0, io.SeekCurrent); err != nil || offset != 17 {
		t.Fatal("Inspect changed file offset")
	}
	after, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(data, after) || sha256.Sum256(data) != sha256.Sum256(after) {
		t.Fatal("Inspect changed original")
	}
	info, err := os.Stat(name)
	if err != nil || !os.SameFile(before, info) || !info.ModTime().Equal(before.ModTime()) {
		t.Fatal("Inspect changed source identity")
	}
}

func TestInspectErrorsAreZeroStamp(t *testing.T) {
	source, _, _ := adapterFixture(t)
	stamp, err := Inspect(nil, source)
	assertEmptyStamp(t, stamp, err, ErrInvalidInput)
	var adapter *Adapter
	stamp, err = adapter.Inspect(context.Background(), source)
	assertEmptyStamp(t, stamp, err, ErrInvalidInput)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stamp, err = Inspect(ctx, source)
	assertEmptyStamp(t, stamp, err, context.Canceled)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	stamp, err = Inspect(ctx, source)
	assertEmptyStamp(t, stamp, err, context.DeadlineExceeded)
	bad := source
	bad.RelativePath = "../private"
	stamp, err = Inspect(context.Background(), bad)
	assertEmptyStamp(t, stamp, err, ErrInvalidInput)
	bad.RelativePath = "missing.mp4"
	stamp, err = Inspect(context.Background(), bad)
	assertEmptyStamp(t, stamp, err, ErrUnavailable)
	bad.RelativePath = "folder"
	stamp, err = Inspect(context.Background(), bad)
	assertEmptyStamp(t, stamp, err, ErrUnavailable)
	input, err := Open(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	stamp, err = inspectOpened(context.Background(), source, input)
	assertEmptyStamp(t, stamp, err, ErrUnavailable)
	stamp, err = inspectOpened(ctx, source, input)
	assertEmptyStamp(t, stamp, err, context.DeadlineExceeded)
}

func TestInspectDetectsOpenedTargetChanges(t *testing.T) {
	cases := map[string]func(*testing.T, string){
		"size": func(t *testing.T, name string) {
			if err := os.Truncate(name, 7); err != nil {
				t.Fatal(err)
			}
		},
		"mtime": func(t *testing.T, name string) {
			if err := os.Chtimes(name, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}
		},
		"replacement": func(t *testing.T, name string) {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(name, name+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		},
		"removed": func(t *testing.T, name string) {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, name string) {
			if err := os.Rename(name, name+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(name, 0700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			source, path, _ := adapterFixture(t)
			input, err := Open(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			change(t, path)
			stamp, err := inspectOpened(context.Background(), source, input)
			assertEmptyStamp(t, stamp, err, ErrChanged)
		})
	}
}

func TestInspectDetectsSymlinkReplacement(t *testing.T) {
	source, name, data := adapterFixture(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	testLink := filepath.Join(source.RootPath, "privilege-check")
	if err := os.Symlink(target, testLink); err != nil {
		t.Skip("platform does not permit symlink creation")
	}
	if err := os.Remove(testLink); err != nil {
		t.Fatal(err)
	}
	input, err := Open(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	stamp, err := inspectOpened(context.Background(), source, input)
	assertEmptyStamp(t, stamp, err, ErrChanged)
	stamp, err = Inspect(context.Background(), source)
	assertEmptyStamp(t, stamp, err, ErrUnavailable)
}

func TestInspectEdgesChangeKeyAndDocumentsQuickKeyLimits(t *testing.T) {
	source, name, data := adapterFixture(t)
	ctx := context.Background()
	before, err := Inspect(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	writeAt := func(offset int64) {
		file, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteAt([]byte{0x42}, offset)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("mutation fixture failed")
		}
		if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(0)
	first, err := Inspect(ctx, source)
	if err != nil || first.Fingerprint == before.Fingerprint {
		t.Fatal("first edge not reflected in key")
	}
	writeAt(int64(len(data) - 1))
	last, err := Inspect(ctx, source)
	if err != nil || last.Fingerprint == first.Fingerprint {
		t.Fatal("last edge not reflected in key")
	}
	writeAt(int64(len(data) / 2))
	middle, err := Inspect(ctx, source)
	if err != nil || middle != last {
		t.Fatal("quick key limitation changed: unsampled middle should be invisible with restored mtime")
	}
	current, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, current, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	replaced, err := Inspect(ctx, source)
	if err != nil || replaced != middle {
		t.Fatal("object identity must not be invented as a persistent fingerprint field")
	}
}

func TestParserResultCanBeSafelyPersisted(t *testing.T) {
	result := adapterOutput(500)
	metadata, err := ParseJSON(result.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := domain.MarshalProbeMetadata(metadata)
	if err != nil || len(encoded) == 0 {
		t.Fatal("parser result rejected by persistence boundary", err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"eac3","profile":"Dolby Digital Plus + Dolby Atmos","channels":6,"sample_rate":"48000","channel_layout":"5.1","tags":{"language":"ENG","title":"private","vendor":"private"}}]}`),
		[]byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","r_frame_rate":"0/0","avg_frame_rate":"24000/1001","side_data_list":[{"side_data_type":"Mastering display metadata","red_x":"1/2","max_luminance":"1000/1"}]}],"format":{"format_name":"matroska,webm","filename":"private","duration":"1.000000"},"chapters":[{"id":1,"start_time":"0.000000","end_time":"1.000000","tags":{"title":"private"}}]}`),
	} {
		metadata, err := ParseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := domain.MarshalProbeMetadata(metadata)
		if err != nil || bytes.Contains(encoded, []byte("private")) {
			t.Fatal("normalized parser/persistence mismatch or privacy leak", err)
		}
	}
}

func TestInspectConcurrentMutationAndCancellationNeverReturnPartialStamp(t *testing.T) {
	source, name, data := adapterFixture(t)
	writer, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	workers.Add(1)
	t.Cleanup(func() { cancel(); workers.Wait() })
	writeErrors := make(chan error, 1)
	go func() {
		defer workers.Done()
		defer writer.Close()
		for ctx.Err() == nil {
			for _, size := range []int64{7, int64(len(data))} {
				if err := writer.Truncate(size); err != nil {
					writeErrors <- err
					return
				}
			}
		}
	}()
	for i := 0; i < 128; i++ {
		stamp, err := Inspect(ctx, source)
		if err == nil {
			if domain.ValidateProbeStamp(stamp) != nil {
				t.Fatal("concurrent success returned invalid stamp")
			}
			continue
		}
		if stamp != (domain.ProbeStamp{}) || !errors.Is(err, ErrChanged) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("concurrent failure returned partial stamp or unsafe error", err)
		}
	}
	cancel()
	workers.Wait()
	select {
	case err := <-writeErrors:
		t.Fatal(err)
	default:
	}
	// Cancel an already opened input while inspection is eligible to start.
	// This exercises ownership of the descriptor, including the cancellation
	// callback joining Close; no observation may survive a reported error.
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		input, err := Open(ctx, source)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		type observed struct {
			stamp domain.ProbeStamp
			err   error
		}
		result := make(chan observed, 1)
		go func() { stamp, err := inspectOpened(ctx, source, input); result <- observed{stamp, err} }()
		cancel()
		got := <-result
		if got.err != nil {
			if got.stamp != (domain.ProbeStamp{}) || !errors.Is(got.err, context.Canceled) {
				t.Fatal("cancellation returned partial stamp or wrong classification", got.err)
			}
		} else if domain.ValidateProbeStamp(got.stamp) != nil {
			t.Fatal("invalid racing success")
		}
		_ = input.Close()
		if _, err := input.Stdin().Stat(); err == nil {
			t.Fatal("cancelled inspector leaked descriptor")
		}
	}
}

func TestParserWhitelistsMatchCacheBoundary(t *testing.T) {
	cases := []struct {
		field  string
		values map[string]bool
		kind   string
	}{
		{"codec_name", codecNames, "video"}, {"profile", profiles, "video"},
		{"color_range", colorsRange, "video"}, {"color_space", colorsSpace, "video"},
		{"color_transfer", colorsTransfer, "video"}, {"color_primaries", colorsPrimaries, "video"},
		{"channel_layout", channelLayouts, "audio"},
	}
	for _, tc := range cases {
		for value := range tc.values {
			stream := map[string]any{"index": 0, "codec_type": tc.kind, tc.field: value}
			raw, _ := json.Marshal(map[string]any{"streams": []any{stream}})
			metadata, err := ParseJSON(raw)
			if err != nil {
				t.Fatal("known parser enum rejected", tc.field, value, err)
			}
			if _, err := domain.MarshalProbeMetadata(metadata); err != nil {
				t.Fatal("parser/cache enum drift", tc.field, value, err)
			}
		}
	}
	for name := range formatNames {
		raw, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"index": 0, "codec_type": "data"}}, "format": map[string]string{"format_name": name}})
		metadata, err := ParseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := domain.MarshalProbeMetadata(metadata); err != nil {
			t.Fatal("parser/cache format drift", name, err)
		}
	}
}

func FuzzParserCacheBoundary(f *testing.F) {
	f.Add([]byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":320,"height":180}]}`))
	f.Add([]byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"eac3","profile":"Dolby Digital Plus + Dolby Atmos","channels":6,"sample_rate":"48000"}]}`))
	f.Add([]byte(`{"streams":[{"index":0,"codec_type":"video","r_frame_rate":"30000/1001","side_data_list":[{"side_data_type":"Mastering display metadata","red_x":"1/2","max_luminance":"1000/1"}]}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		metadata, err := ParseJSON(raw)
		if err != nil {
			return
		}
		encoded, err := domain.MarshalProbeMetadata(metadata)
		if err != nil {
			uncapped, _ := json.Marshal(metadata)
			if encoded != nil || len(uncapped) <= domain.ProbeMetadataMaxBytes {
				t.Fatal("valid normalized parser result rejected below byte limit")
			}
		} else if len(encoded) > domain.ProbeMetadataMaxBytes {
			t.Fatal("serialization limit bypassed")
		}
	})
}
