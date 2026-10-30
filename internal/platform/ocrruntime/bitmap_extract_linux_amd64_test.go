package ocrruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// G15.5: the isolated extraction cache holds embedded PGS and VobSub
// tracks byte for byte as the pinned mkvextract writes them when run
// directly, and the original Matroska file is never changed.
func TestRealBitmapSubtitleExtractionMatchesMkvextract(t *testing.T) {
	requireHostRuntime(t, "JELEE_MATROSKA_HOST_RUNTIME")
	root := hostToolsRoot(t)
	media := muxFixture(t, root, []string{"The bridge is closed tonight."}, []string{"Who sent you here?"})
	before := digestFile(t, media)
	runners := hostRunners(t, 1, hostMatroskaRegistration(t, root, "mkvmerge", sandbox.ToolIdentify), hostMatroskaRegistration(t, root, "mkvextract", sandbox.ToolExtract))
	cache := privateDirectory(t, "cache")
	extractor, err := matroska.New(runners[sandbox.ToolIdentify], runners[sandbox.ToolExtract], matroska.Config{CacheRoot: cache})
	if err != nil {
		t.Fatal(err)
	}
	direct := t.TempDir()
	mkvextract := filepath.Join(root, ".bin", "mkvextract")
	skipMissing(t, mkvextract, "make bootstrap-matroska")
	command := exec.Command(mkvextract, "--quiet", media, "tracks", "0:"+filepath.Join(direct, "pgs.sup"), "1:"+filepath.Join(direct, "dvd.idx"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("mkvextract: %v %s", err, output)
	}
	source := domain.ProbeSource{RootPath: filepath.Dir(media), RelativePath: filepath.Base(media)}
	const sourceID = "00000000-0000-4000-8000-0000000000b6"
	for _, check := range []struct {
		kind  matroska.Kind
		index int
		file  string
	}{{matroska.KindSubtitle, 0, "pgs.sup"}, {matroska.KindVobSubIndex, 1, "dvd.idx"}, {matroska.KindVobSubData, 1, "dvd.sub"}} {
		item, err := extractor.Locate(context.Background(), sourceID, source, check.kind, check.index)
		if err != nil {
			t.Fatalf("%s: %v", check.file, err)
		}
		cached, want := digestFile(t, filepath.Join(cache, filepath.FromSlash(item.RelativePath))), digestFile(t, filepath.Join(direct, check.file))
		info, _ := os.Stat(filepath.Join(direct, check.file))
		if cached != want || info.Size() == 0 {
			t.Fatalf("%s: cached %s, mkvextract %s (%d bytes)", check.file, cached, want, info.Size())
		}
		t.Logf("%s: %d bytes, sha256 %s", check.file, info.Size(), want)
	}
	if _, err := extractor.Locate(context.Background(), sourceID, source, matroska.KindSubtitle, 1); err != matroska.ErrNotFound {
		t.Fatalf("VobSub as one file: %v", err)
	}
	if digestFile(t, media) != before {
		t.Fatal("the original Matroska file changed")
	}
}
