package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// coverRealTool loads the explicit developer profile used by the sandbox
// real-tool test: the manifest-pinned ffprobe and its exact library closure.
// The pinned ffmpeg beside it only generates fixtures in the test directory.
func coverRealTool(t *testing.T) (sandbox.Profile, sandbox.Policy, string) {
	t.Helper()
	path := os.Getenv("JELEE_SANDBOX_REAL_PROFILE")
	if path == "" {
		if os.Getenv("JELEE_REQUIRE_SANDBOX_TEST") == "true" {
			t.Fatal("embedded cover real-tool test requires JELEE_SANDBOX_REAL_PROFILE")
		}
		t.Skip("embedded cover real-tool test NOT RUN: no explicit developer profile; no host tool is trusted automatically")
	}
	var fixture struct {
		Profile sandbox.Profile
		Policy  sandbox.Policy
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &fixture) != nil {
		t.Fatal("invalid explicit real-tool profile")
	}
	var manifest struct {
		MediaTools struct {
			Platforms map[string]struct {
				Executables map[string]struct{ SHA256 string } `json:"executables"`
			} `json:"platforms"`
		} `json:"mediaTools"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "manifest.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("tool manifest unavailable")
	}
	pinned := manifest.MediaTools.Platforms["linux-amd64"].Executables
	ffmpeg := filepath.Join(filepath.Dir(fixture.Profile.FFprobePath), "ffmpeg")
	if coverFileDigest(t, fixture.Profile.FFprobePath) != pinned["ffprobe"].SHA256 || fixture.Policy.FFprobeSHA256 != pinned["ffprobe"].SHA256 || coverFileDigest(t, ffmpeg) != pinned["ffmpeg"].SHA256 {
		t.Fatal("real tools differ from the manifest pins")
	}
	return fixture.Profile, fixture.Policy, ffmpeg
}

func coverFileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("open pinned file")
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal("hash pinned file")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func coverWriteImage(t *testing.T, path string, encode func(io.Writer) error) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := encode(&buffer); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func coverGradient(width, height int, shift uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x*4) + shift, uint8(y * 4), 128, 255})
		}
	}
	return img
}

// coverFFmpeg generates one fixture with an argument array; no shell.
func coverFFmpeg(t *testing.T, ffmpeg string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-threads", "1", "-filter_threads", "1"}, args...)...)
	command.Env = []string{"LANG=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v %s", err, output)
	}
}

type coverRealFixture struct {
	probeFixture
	root    string
	adapter *probe.Adapter
}

// probeAndSeed runs the real sandboxed metadata probe, seeds the probe cache
// with its observation and returns the observed metadata.
func (f coverRealFixture) probeAndSeed(t *testing.T, relative string) domain.MediaMetadata {
	t.Helper()
	observation, err := f.adapter.Probe(f.ctx, probe.Source{RootPath: f.root, RelativePath: relative})
	if err != nil {
		t.Fatal("real probe", err)
	}
	encoded, err := domain.MarshalProbeMetadata(observation.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	seedCoverProbe(t, f.probeFixture, relative, observation.File.Size, observation.File.ModifiedUnixNano, observation.Fingerprint, string(encoded))
	return observation.Metadata
}

// coverPass mirrors the catalog sync pass: page, extract outside any
// transaction, store the original, record.
func coverPass(t *testing.T, f probeFixture, l domain.JobLease, extractor *probe.CoverAdapter, store *images.Store) map[string]string {
	t.Helper()
	outcomes := map[string]string{}
	after := ""
	for {
		page, err := f.s.NextEmbeddedCoverCandidates(f.ctx, l, after, domain.EmbeddedCoverBatch)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Candidates {
			result := domain.EmbeddedCoverResult{Candidate: c}
			cover, err := extractor.ExtractCover(f.ctx, c)
			switch {
			case err == nil:
				digest, size, err := store.PutOriginal(f.ctx, bytes.NewReader(cover.Data), domain.EmbeddedCoverMaxBytes)
				if err != nil || digest != cover.SHA256 {
					t.Fatal("store original", err)
				}
				result.Outcome = domain.EmbeddedCoverStored
				result.Content = &domain.ItemImageContent{SHA256: digest[:], Width: cover.Width, Height: cover.Height, Format: cover.Format, Bytes: size, FetchedAt: time.Now().UTC().Truncate(time.Microsecond)}
			case errors.Is(err, domain.ErrEmbeddedCoverTooLarge):
				result.Outcome = domain.EmbeddedCoverTooLarge
			case errors.Is(err, domain.ErrEmbeddedCoverInvalid):
				result.Outcome = domain.EmbeddedCoverInvalid
			case errors.Is(err, domain.ErrEmbeddedCoverAbsent):
				result.Outcome = domain.EmbeddedCoverAbsent
			default:
				t.Fatal("extraction", err)
			}
			outcome, err := f.s.RecordEmbeddedCover(f.ctx, l, result)
			if err != nil {
				t.Fatal("record", err)
			}
			outcomes[c.RelativePath] = outcome
		}
		if page.Next == "" {
			return outcomes
		}
		after = page.Next
	}
}

func TestEmbeddedCoverRealFFprobeExtractionIntoStore(t *testing.T) {
	profile, policy, ffmpeg := coverRealTool(t)
	f := coverRealFixture{probeFixture: newProbeFixture(t)}
	f.root = syncRootPath(t, f.jobFixture)
	scratch := t.TempDir()
	jpegCover := coverWriteImage(t, filepath.Join(scratch, "cover.jpg"), func(w io.Writer) error { return jpeg.Encode(w, coverGradient(64, 48, 0), &jpeg.Options{Quality: 90}) })
	jpegCover2 := coverWriteImage(t, filepath.Join(scratch, "cover2.jpg"), func(w io.Writer) error { return jpeg.Encode(w, coverGradient(64, 48, 40), &jpeg.Options{Quality: 90}) })
	pngCover := coverWriteImage(t, filepath.Join(scratch, "cover.png"), func(w io.Writer) error { return png.Encode(w, coverGradient(40, 30, 0)) })
	noise := image.NewRGBA(image.Rect(0, 0, 1100, 1000))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range noise.Pix {
		noise.Pix[i] = byte(random.Uint32())
		if i%4 == 3 {
			noise.Pix[i] = 255
		}
	}
	big := coverWriteImage(t, filepath.Join(scratch, "big.png"), func(w io.Writer) error {
		return (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(w, noise)
	})
	if len(big) <= domain.EmbeddedCoverMaxBytes {
		t.Fatal("oversized fixture is not oversized")
	}
	source := []string{"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10", "-t", "1"}
	mp4 := func(cover, out string) {
		coverFFmpeg(t, ffmpeg, append(append([]string(nil), source[:4]...), "-i", cover, "-t", "1", "-map", "0:v", "-map", "1:v", "-c:v:0", "mpeg4", "-c:v:1", "copy", "-disposition:v:1", "attached_pic", out)...)
	}
	mkv := func(cover, out string) {
		coverFFmpeg(t, ffmpeg, append(append([]string(nil), source...), "-map", "0:v", "-c:v", "mpeg4", "-attach", cover, "-metadata:s:t", "mimetype=image/png", "-metadata:s:t", "filename=cover.png", out)...)
	}
	for _, dir := range []string{"Atom", "Attachment", "Big", "Plain"} {
		if err := os.MkdirAll(filepath.Join(f.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	mp4(filepath.Join(scratch, "cover.jpg"), filepath.Join(f.root, "Atom", "Atom.mp4"))
	mkv(filepath.Join(scratch, "cover.png"), filepath.Join(f.root, "Attachment", "Attachment.mkv"))
	mkv(filepath.Join(scratch, "big.png"), filepath.Join(f.root, "Big", "Big.mkv"))
	coverFFmpeg(t, ffmpeg, append(append([]string(nil), source...), "-c:v", "mpeg4", filepath.Join(f.root, "Plain", "Plain.mp4"))...)

	temp := t.TempDir()
	policyData, err := json.Marshal(policy)
	if err != nil || os.WriteFile(filepath.Join(temp, coverHelperPolicyFile), policyData, 0600) != nil {
		t.Fatal("write helper policy")
	}
	launcher, err := sandbox.New(f.ctx, profile, policy)
	if err != nil {
		t.Fatal("real launcher", err)
	}
	config := process.Config{MaxConcurrent: 1, Timeout: 30 * time.Second, MaxStdoutBytes: 4 << 20, MaxStderrBytes: 64 << 10, TempRoot: temp}
	metadataRunner, err := process.NewIsolatedFFprobe(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if f.adapter, err = probe.NewAdapter(metadataRunner, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	config.MaxStdoutBytes = 16 << 20
	coverRunner, err := process.NewIsolatedFFprobeCover(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	extractor, err := probe.NewCoverAdapter(coverRunner)
	if err != nil {
		t.Fatal(err)
	}

	items := map[string]string{}
	for _, relative := range []string{"Atom/Atom.mp4", "Attachment/Attachment.mkv", "Big/Big.mkv", "Plain/Plain.mp4"} {
		meta := f.probeAndSeed(t, relative)
		_, video, ok := domain.EmbeddedCoverStream(meta)
		if ok != (relative != "Plain/Plain.mp4") || ok && video != 1 {
			t.Fatal("probe did not report the attached picture", relative)
		}
		items[relative] = metadataItem(t, f.jobFixture)
		sidecarSource(t, f.jobFixture, items[relative], relative)
	}
	mediaDigests := map[string]string{}
	for relative := range items {
		mediaDigests[relative] = coverFileDigest(t, filepath.Join(f.root, filepath.FromSlash(relative)))
	}
	storeRoot := t.TempDir()
	if err := os.Chmod(storeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := images.OpenStore(f.ctx, images.StoreOptions{Root: storeRoot, MediaRoots: []string{f.root}, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	l := coverSyncLease(t, f.probeFixture, "covers-real")

	outcomes := coverPass(t, f.probeFixture, l, extractor, store)
	if len(outcomes) != 3 || outcomes["Atom/Atom.mp4"] != domain.EmbeddedCoverStored || outcomes["Attachment/Attachment.mkv"] != domain.EmbeddedCoverStored || outcomes["Big/Big.mkv"] != domain.EmbeddedCoverTooLarge {
		t.Fatal("real pass outcomes", outcomes)
	}
	// Not transcoded: the stored original is the embedded stream byte for byte.
	assertStored := func(relative string, want []byte, format string, width, height int) {
		t.Helper()
		var digest []byte
		var gotFormat string
		var w, h int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT content_sha256,format,width,height FROM item_images WHERE item_id=$1::uuid AND source_kind='embedded' AND image_type='Primary'`, items[relative]).Scan(&digest, &gotFormat, &w, &h); err != nil {
			t.Fatal(relative, err)
		}
		sum := sha256.Sum256(want)
		if !bytes.Equal(digest, sum[:]) || gotFormat != format || w != width || h != height {
			t.Fatal("stored picture differs from the embedded bytes", relative)
		}
		object, err := store.OpenOriginal(f.ctx, sum, true)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := io.ReadAll(object)
		_ = object.Close()
		if err != nil || !bytes.Equal(stored, want) {
			t.Fatal("store original is not the embedded picture", relative)
		}
	}
	assertStored("Atom/Atom.mp4", jpegCover, "jpeg", 64, 48)
	assertStored("Attachment/Attachment.mkv", pngCover, "png", 40, 30)
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid`, items["Big/Big.mkv"]) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM item_embedded_cover_attempts WHERE item_id=$1::uuid AND outcome='too_large'`, items["Big/Big.mkv"]) != 1 {
		t.Fatal("oversized cover stored or not remembered")
	}
	for relative, digest := range mediaDigests {
		if coverFileDigest(t, filepath.Join(f.root, filepath.FromSlash(relative))) != digest {
			t.Fatal("extraction changed a media file", relative)
		}
	}

	// Unchanged fingerprints start no process at all.
	started := coverRunner.Stats().Started
	if again := coverPass(t, f.probeFixture, l, extractor, store); len(again) != 0 || coverRunner.Stats().Started != started {
		t.Fatal("unchanged files extracted again", again)
	}
	// A rewritten file with a new cover is probed, offered and replaced.
	mp4(filepath.Join(scratch, "cover2.jpg"), filepath.Join(f.root, "Atom", "Atom.next.mp4"))
	if err := os.Rename(filepath.Join(f.root, "Atom", "Atom.next.mp4"), filepath.Join(f.root, "Atom", "Atom.mp4")); err != nil {
		t.Fatal(err)
	}
	f.probeAndSeed(t, "Atom/Atom.mp4")
	if again := coverPass(t, f.probeFixture, l, extractor, store); len(again) != 1 || again["Atom/Atom.mp4"] != domain.EmbeddedCoverStored {
		t.Fatal("changed file not extracted again", again)
	}
	assertStored("Atom/Atom.mp4", jpegCover2, "jpeg", 64, 48)

	// Bounded failures, each without remembering anything.
	attachment := f.probeAndSeed(t, "Attachment/Attachment.mkv")
	stream, video, _ := domain.EmbeddedCoverStream(attachment)
	info, err := os.Stat(filepath.Join(f.root, "Attachment", "Attachment.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := f.adapter.Probe(f.ctx, probe.Source{RootPath: f.root, RelativePath: "Attachment/Attachment.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	candidate := domain.EmbeddedCoverCandidate{ItemID: items["Attachment/Attachment.mkv"], LibraryID: f.registration.Library.ID, RootID: f.registration.RootID, RootPath: f.root,
		RelativePath: "Attachment/Attachment.mkv", StreamIndex: stream, VideoIndex: video,
		Stamp: domain.ProbeStamp{Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano(), Fingerprint: observation.Fingerprint, FingerprintVersion: domain.ProbeFingerprintVersion}}
	if cover, err := extractor.ExtractCover(f.ctx, candidate); err != nil || !bytes.Equal(cover.Data, pngCover) {
		t.Fatal("direct extraction", err)
	}
	t.Run("main_video_stream_is_absent", func(t *testing.T) {
		c := candidate
		c.StreamIndex, c.VideoIndex = 0, 0
		if _, err := extractor.ExtractCover(f.ctx, c); !errors.Is(err, domain.ErrEmbeddedCoverAbsent) {
			t.Fatal("ordinary video stream treated as a cover", err)
		}
	})
	t.Run("changed_fingerprint_starts_nothing", func(t *testing.T) {
		c := candidate
		c.Stamp.Fingerprint = coverFingerprint("stale")
		before := coverRunner.Stats().Started
		if _, err := extractor.ExtractCover(f.ctx, c); !errors.Is(err, domain.ErrProbeSourceChanged) || coverRunner.Stats().Started != before {
			t.Fatal("stale stamp extracted", err)
		}
	})
	t.Run("output_limit", func(t *testing.T) {
		limited := config
		limited.MaxStdoutBytes = 1024
		runner, err := process.NewIsolatedFFprobeCover(limited, launcher)
		if err != nil {
			t.Fatal(err)
		}
		small, _ := probe.NewCoverAdapter(runner)
		if _, err := small.ExtractCover(f.ctx, candidate); !errors.Is(err, domain.ErrEmbeddedCoverTooLarge) {
			t.Fatal("output limit not a refusal", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		limited := config
		limited.Timeout = time.Millisecond
		runner, err := process.NewIsolatedFFprobeCover(limited, launcher)
		if err != nil {
			t.Fatal(err)
		}
		slow, _ := probe.NewCoverAdapter(runner)
		if _, err := slow.ExtractCover(f.ctx, candidate); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("timeout not reported as a transient deadline", err)
		}
		if runner.Stats().Active != 0 {
			t.Fatal("timed-out child survived")
		}
	})
}
