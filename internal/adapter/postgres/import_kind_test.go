package postgres

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestImportVideoCLIKindAndNFO(t *testing.T) {
	ctx, store, dsn := accountTestStore(t)
	if _, err := store.BootstrapAdmin(ctx, accountInput("import-admin")); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, ctx, store, "import-admin"))
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "jelee-cli")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../../../cmd/jelee-cli") //nolint:staticcheck // SA1019: test helpers build with the toolchain running the test; project wrappers export GOROOT
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual import CLI: %v: %s", err, output)
	}
	credentialFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(credentialFile, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	registration, err := store.RegisterLibrary(ctx, "Import kinds", root)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.GetNFOLibraryPolicy(ctx, actor, registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetNFOLibraryPolicy(ctx, actor, policy.LibraryID, "enable-import-nfo", policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	run := func(file, kind string) error {
		args := []string{"import-video", "--library", "Import kinds", "--root", root, "--file", file, "--title", "Imported title"}
		if kind != "" {
			args = append(args, "--kind", kind)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "JELEE_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "JELEE_DATABASE_URL_FILE="+credentialFile)
		return cmd.Run()
	}
	for _, kind := range []string{"Episode", "Movie", "HomeVideo", ""} {
		name := kind
		if name == "" {
			name = "Default"
		}
		t.Run(name, func(t *testing.T) {
			video := name + ".mkv"
			content := []byte("original media for " + name)
			document := `<movie><title>NFO title</title></movie>`
			if kind == "Episode" {
				document = `<episodedetails><title>NFO title</title><season>0</season><episode>7</episode></episodedetails>`
			}
			if err := os.WriteFile(filepath.Join(root, video), content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name+".nfo"), []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(video, kind); err != nil {
				t.Fatal("actual CLI cannot register video kind", name, err)
			}
			var item, actualKind string
			if err := store.Pool.QueryRow(ctx, `SELECT i.id::text,i.kind FROM items i JOIN media_sources s ON s.item_id=i.id WHERE s.relative_path=$1`, video).Scan(&item, &actualKind); err != nil {
				t.Fatal(err)
			}
			want := kind
			if want == "" {
				want = "HomeVideo"
			}
			if actualKind != want {
				t.Fatal("CLI changed requested item kind", actualKind)
			}
			scope, err := store.ResolveItemNFO(ctx, actor, item, 1)
			if err != nil {
				t.Fatal(err)
			}
			reader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
			observed, err := reader.ObserveItemNFO(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			state := observed.(interface {
				State() domain.NFOItemObservationState
			}).State()
			result, err := store.ApplyItemNFOObservation(ctx, actor, scope, state)
			if want == "HomeVideo" {
				want = "Movie" // Existing confirmed movie NFO classification.
			}
			if err != nil || result.Metadata.Revision != 2 || result.Metadata.Kind != want || result.Metadata.Fields[0].Value != "NFO title" {
				t.Fatal("imported video could not apply its NFO", err)
			}
			if kind == "Episode" && (string(sourceRatingFact(t, result.Metadata, "seasonNumber").Value) != "0" || string(sourceRatingFact(t, result.Metadata, "episodeNumber").Value) != "7") {
				t.Fatal("imported episode lost numbering")
			}
			before := integrationCounts(t, ctx, store)
			if run(video, kind) == nil || integrationCounts(t, ctx, store) != before {
				t.Fatal("duplicate CLI import left partial catalog state")
			}
			if raw, err := os.ReadFile(filepath.Join(root, video)); err != nil || string(raw) != string(content) {
				t.Fatal("CLI changed original media", err)
			}
			if raw, err := os.ReadFile(filepath.Join(root, name+".nfo")); err != nil || string(raw) != document {
				t.Fatal("CLI changed original NFO", err)
			}
		})
	}
	before := integrationCounts(t, ctx, store)
	for _, kind := range []string{"Series", "Season", "episode", "Unknown"} {
		file := "invalid-" + kind + ".mkv"
		if err := os.WriteFile(filepath.Join(root, file), []byte("original invalid-kind media"), 0600); err != nil {
			t.Fatal(err)
		}
		if run(file, kind) == nil || integrationCounts(t, ctx, store) != before {
			t.Fatal("CLI accepted a folder or invalid kind", kind)
		}
	}
}

func TestImportVideoKindRejectsInvalidWithoutWrites(t *testing.T) {
	ctx, store, _ := accountTestStore(t)
	before := integrationCounts(t, ctx, store)
	for _, kind := range []string{"", "Series", "Season", "episode", "Unknown", " Movie"} {
		_, err := store.ImportVideoKind(ctx, "Rejected kind", "/synthetic/import-kind", "new.mkv", "Title", "video/x-matroska", kind)
		if err != domain.ErrInvalid || integrationCounts(t, ctx, store) != before {
			t.Fatal("invalid kind reached catalog transaction", kind, err)
		}
	}
}
