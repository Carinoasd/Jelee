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

func TestDirectoryImportCLIAndNFO(t *testing.T) {
	ctx, store, dsn := accountTestStore(t)
	if _, err := store.BootstrapAdmin(ctx, accountInput("directory-admin")); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, ctx, store, "directory-admin"))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Series", "Season 0"), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := store.RegisterLibrary(ctx, "Directories", root)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.GetNFOLibraryPolicy(ctx, actor, registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.SetNFOLibraryPolicy(ctx, actor, policy.LibraryID, "directory-nfo", policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "jelee-cli")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "../../../cmd/jelee-cli").CombinedOutput(); err != nil {
		t.Fatalf("build directory CLI: %v: %s", err, output)
	}
	credentialFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(credentialFile, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	parent := ""
	for _, entry := range []struct{ kind, directory, name, xml string }{
		{"Series", "Series", "TVSHOW.NFO", `<tvshow><title>Series NFO</title><season>1</season></tvshow>`},
		{"Season", "Series/Season 0", "SEASON.NFO", `<season><title>Specials</title><seasonnumber>0</seasonnumber><plot>Season plot</plot></season>`},
	} {
		file := filepath.Join(root, filepath.FromSlash(entry.directory), entry.name)
		if err := os.WriteFile(file, []byte(entry.xml), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"import-directory", "--library", "Directories", "--root", root, "--directory", entry.directory, "--title", "Directory title", "--kind", entry.kind}
		if parent != "" {
			args = append(args, "--parent", parent)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "JELEE_") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "JELEE_DATABASE_URL_FILE="+credentialFile)
		if err := cmd.Run(); err != nil {
			t.Fatal("actual CLI cannot import directory item", entry.kind, err)
		}
		var item string
		if err := store.Pool.QueryRow(ctx, `SELECT id::text FROM items WHERE library_id=$1::uuid AND kind=$2`, registration.Library.ID, entry.kind).Scan(&item); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM media_sources WHERE item_id=$1::uuid`, item).Scan(&count); err != nil || count != 0 {
			t.Fatal("directory import fabricated a media source", err)
		}
		scope, err := store.ResolveItemNFO(ctx, actor, item, 1)
		if err != nil {
			t.Fatal("directory NFO scope unavailable", entry.kind, err)
		}
		reader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
		observed, err := reader.ObserveItemNFO(ctx, scope)
		if err != nil {
			t.Fatal("directory NFO observation failed", entry.kind, err)
		}
		state := observed.(interface {
			State() domain.NFOItemObservationState
		}).State()
		result, err := store.ApplyItemNFOObservation(ctx, actor, scope, state)
		if err != nil || result.Metadata.Revision != 2 || result.Metadata.Kind != entry.kind {
			t.Fatal("directory NFO apply failed", entry.kind, err)
		}
		if entry.kind == "Season" {
			if string(sourceRatingFact(t, result.Metadata, "seasonNumber").Value) != "0" {
				t.Fatal("season zero was lost")
			}
			var actualParent string
			if err := store.Pool.QueryRow(ctx, `SELECT parent_id::text FROM item_parent_links WHERE item_id=$1::uuid`, item).Scan(&actualParent); err != nil || actualParent != parent {
				t.Fatal("season parent binding was lost", err)
			}
			catalog, err := store.GetItem(ctx, actor.UserID, item)
			if err != nil || catalog.ParentID != parent {
				t.Fatal("catalog detail omitted parent", err)
			}
			items, err := store.ListItems(ctx, actor.UserID, "", 100)
			found := false
			for _, value := range items {
				found = found || value.ID == item && value.ParentID == parent
			}
			if err != nil || !found {
				t.Fatal("catalog list omitted parent", err)
			}
		}
		parent = item
		if raw, err := os.ReadFile(file); err != nil || string(raw) != entry.xml {
			t.Fatal("directory import changed NFO", err)
		}
	}
	video := filepath.Join(root, "Series", "Season 0", "Episode.mkv")
	if err := os.WriteFile(video, []byte("original linked episode"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "import-video", "--library", "Directories", "--root", root, "--file", "Series/Season 0/Episode.mkv", "--kind", "Episode", "--title", "Linked episode", "--parent", parent)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "JELEE_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "JELEE_DATABASE_URL_FILE="+credentialFile)
	if err := cmd.Run(); err != nil {
		t.Fatal("actual CLI could not link an episode to its season", err)
	}
	var actualParent string
	if err := store.Pool.QueryRow(ctx, `SELECT p.parent_id::text FROM item_parent_links p JOIN items i ON i.id=p.item_id WHERE i.kind='Episode' AND i.library_id=$1::uuid`, registration.Library.ID).Scan(&actualParent); err != nil || actualParent != parent {
		t.Fatal("CLI episode parent mismatch", err)
	}
}
