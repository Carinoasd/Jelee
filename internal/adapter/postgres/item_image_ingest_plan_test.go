package postgres

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	planRoot   = "11111111-1111-4111-8111-111111111111"
	planMovie  = "22222222-2222-4222-8222-222222222222"
	planOther  = "33333333-3333-4333-8333-333333333333"
	planSeries = "44444444-4444-4444-8444-444444444444"
	planSeason = "55555555-5555-4555-8555-555555555555"
	planSpecs  = "66666666-6666-4666-8666-666666666666"
	planEp     = "77777777-7777-4777-8777-777777777777"
)

func planListing(dir string, names ...string) ingestListing {
	var l ingestListing
	for i, name := range names {
		p := name
		if dir != "" {
			p = dir + "/" + name
		}
		l.files = append(l.files, ingestFile{path: p, size: int64(100 + i), modified: int64(1000 + i)})
	}
	return l
}

func emptyIngestInputs() ingestInputs {
	return ingestInputs{parents: map[string]*ingestDir{}, listings: map[ingestDir]ingestListing{}, videos: map[ingestDir]int{},
		art: map[string]ingestArt{}, nfoFiles: map[ingestDir]ingestFile{}}
}

// planned renders an item's plan as "Type/index/kind=path-or-url" lines.
func planned(w *ingestWant) []string {
	var out []string
	for slot, in := range w.inputs {
		ref := in.RelativePath + in.RemoteURL
		out = append(out, fmt.Sprintf("%s/%d/%s=%s", slot.typ, slot.index, slot.kind, ref))
	}
	slices.Sort(out)
	return out
}

func expectPlan(t *testing.T, label string, w *ingestWant, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := planned(w); !slices.Equal(got, want) {
		t.Fatalf("%s plan\n got %q\nwant %q", label, got, want)
	}
}

func TestPlanItemImagesMovieFolders(t *testing.T) {
	in := emptyIngestInputs()
	single, shared := ingestDir{planRoot, "Inception (2010)"}, ingestDir{planRoot, "Collection"}
	in.items = []ingestItem{
		{id: planMovie, kind: "Movie", sources: []ingestSource{{"s1", planRoot, "Inception (2010)/Inception (2010).mkv"}}},
		{id: planOther, kind: "HomeVideo", sources: []ingestSource{{"s2", planRoot, "Collection/Alien (1979).mkv"}}},
	}
	in.listings[single] = planListing(single.dir, "poster.jpg", "Inception (2010)-poster.png", "fanart.jpg", "fanart1.jpg", "clearlogo.png", "notes.png")
	in.listings[shared] = planListing(shared.dir, "poster.jpg", "fanart.jpg", "Alien (1979)-poster.jpg", "Aliens (1986)-poster.jpg")
	in.videos[single], in.videos[shared] = 1, 2
	all, movies := ingestDirectories(in.items, in.parents)
	if len(all) != 2 || len(movies) != 2 {
		t.Fatal("directories", all, movies)
	}
	plan := planItemImages(in)
	expectPlan(t, "single movie", plan[planMovie],
		"Primary/0/local=Inception (2010)/Inception (2010)-poster.png", "Backdrop/0/local=Inception (2010)/fanart.jpg",
		"Backdrop/1/local=Inception (2010)/fanart1.jpg", "ClearLogo/0/local=Inception (2010)/clearlogo.png")
	// Two movies share the folder: only the video's own name counts.
	expectPlan(t, "shared folder", plan[planOther], "Primary/0/local=Collection/Alien (1979)-poster.jpg")
	if in := plan[planMovie].inputs[ingestSlot{"Primary", 0, domain.ImageSourceLocal}]; *in.SourceSize != 101 || *in.SourceModifiedUnixNano != 1001 || in.RootID != planRoot {
		t.Fatalf("file attributes %+v", in)
	}

	// The root folder is "", and a video name the matcher refuses still
	// leaves the folder names usable.
	in = emptyIngestInputs()
	root := ingestDir{planRoot, ""}
	in.items = []ingestItem{{id: planMovie, kind: "Movie", sources: []ingestSource{{"s1", planRoot, "Odd\x01Name.mkv"}}}}
	in.listings[root] = planListing("", "folder.jpg", "Odd\x01Name-poster.jpg")
	in.videos[root] = 1
	plan = planItemImages(in)
	expectPlan(t, "root folder", plan[planMovie], "Primary/0/local=folder.jpg")
	if !plan[planMovie].localComplete {
		t.Fatal("fallback marked the folder incomplete")
	}
}

func TestPlanItemImagesIncompleteAndConflictingFolders(t *testing.T) {
	in := emptyIngestInputs()
	d := ingestDir{planRoot, "Big"}
	in.items = []ingestItem{{id: planMovie, kind: "Movie", sources: []ingestSource{{"s1", planRoot, "Big/Big.mkv"}}}}
	names := make([]string, 0, 10001)
	for i := 0; i < 10001; i++ {
		names = append(names, fmt.Sprintf("img%05d.jpg", i))
	}
	in.listings[d] = planListing(d.dir, names...)
	plan := planItemImages(in)
	if plan[planMovie].localComplete || len(plan[planMovie].inputs) != 0 {
		t.Fatal("an over-long listing was used", plan[planMovie].localComplete, planned(plan[planMovie]))
	}

	in.listings[d] = planListing(d.dir, "logo.png", "LOGO.png", "banner.jpg")
	plan = planItemImages(in)
	expectPlan(t, "conflict", plan[planMovie], "Banner/0/local=Big/banner.jpg")
	if !plan[planMovie].protected[ingestSlot{"Logo", 0, domain.ImageSourceLocal}] || !plan[planMovie].localComplete {
		t.Fatal("conflicting slot not protected")
	}
}

func TestPlanItemImagesSeriesSeasonsAndEpisodes(t *testing.T) {
	in := emptyIngestInputs()
	series, season1, specials := ingestDir{planRoot, "Show"}, ingestDir{planRoot, "Show/Season 01"}, ingestDir{planRoot, "Show/Specials"}
	one, zero := 1, 0
	in.items = []ingestItem{
		{id: planSeries, kind: "Series", dir: &series},
		{id: planSeason, kind: "Season", parent: planSeries, season: &one, dir: &season1},
		{id: planSpecs, kind: "Season", parent: planSeries, season: &zero, dir: &specials},
		{id: planEp, kind: "Episode", parent: planSeason, sources: []ingestSource{{"s1", planRoot, "Show/Season 01/Show S01E01.mkv"}, {"s2", planRoot, "Show/Season 01/Show S01E01 - 4k.mkv"}}},
		{id: planOther, kind: "Season", parent: planSeries},
	}
	in.parents[planSeries] = &series
	in.listings[series] = planListing(series.dir, "poster.jpg", "fanart.jpg", "season01-poster.jpg", "season01-thumb.jpg",
		"season-all-banner.jpg", "season-all-poster.jpg", "season-specials-poster.jpg", "season00-landscape.jpg")
	in.listings[season1] = planListing(season1.dir, "folder.jpg", "Show S01E01-thumb.jpg", "Show S01E01 - 4k.episode-thumb.png")
	all, movies := ingestDirectories(in.items, in.parents)
	if len(all) != 3 || len(movies) != 0 {
		t.Fatal("directories", all, movies)
	}
	plan := planItemImages(in)
	expectPlan(t, "series", plan[planSeries], "Primary/0/local=Show/poster.jpg", "Backdrop/0/local=Show/fanart.jpg")
	// The season folder beats seasonNN-*, which beats season-all-*.
	expectPlan(t, "season 1", plan[planSeason], "Primary/0/local=Show/Season 01/folder.jpg", "Thumb/0/local=Show/season01-thumb.jpg",
		"Banner/0/local=Show/season-all-banner.jpg")
	expectPlan(t, "specials", plan[planSpecs], "Primary/0/local=Show/season-specials-poster.jpg", "Landscape/0/local=Show/season00-landscape.jpg",
		"Banner/0/local=Show/season-all-banner.jpg")
	// A season without a known number only gets season-all-*.
	expectPlan(t, "unnumbered season", plan[planOther], "Banner/0/local=Show/season-all-banner.jpg", "Primary/0/local=Show/season-all-poster.jpg")
	// The first version's thumb wins the slot.
	expectPlan(t, "episode", plan[planEp], "Thumb/0/local=Show/Season 01/Show S01E01-thumb.jpg")
}

func TestPlanItemImagesNFOArt(t *testing.T) {
	in := emptyIngestInputs()
	movieDir, series := ingestDir{planRoot, "Inception (2010)"}, ingestDir{planRoot, "Show"}
	two := 2
	in.items = []ingestItem{
		{id: planMovie, kind: "Movie", sources: []ingestSource{{"s1", planRoot, "Inception (2010)/Inception (2010).mkv"}}},
		{id: planSeries, kind: "Series", dir: &series},
		{id: planSeason, kind: "Season", parent: planSeries, season: &two},
		{id: planEp, kind: "Episode", sources: []ingestSource{{"s2", planRoot, "Show/Season 02/Show S02E01.mkv"}}},
		{id: planOther, kind: "Movie"},
	}
	in.parents[planSeries] = &series
	in.art[planMovie] = ingestArt{dir: &movieDir, entries: []domain.NFOArtwork{
		{Kind: "Poster", Location: "Inception (2010)-poster.png"},
		{Kind: "fanart", Location: "../../outside.jpg"},
		{Kind: "fanart", Location: "https://img.example/f0.jpg"},
		{Kind: "fanart", Location: "extrafanart%2Ffanart%202.jpg"},
		{Kind: "fanart", Location: `extrafanart\fanart3.jpg`},
		{Kind: "fanart", Location: "extrafanart/absent.jpg"},
		{Kind: "fanart", Location: "../Shared/fanart.jpg"},
		{Kind: "clearlogo", Location: "http://insecure.example/logo.png"},
		{Kind: "banner", Location: "/etc/banner.jpg"},
		{Kind: "banner", Location: "C:banner.jpg"},
		{Kind: "banner", Location: "smb://host/banner.jpg"},
		{Kind: "banner", Location: "%zz.jpg"},
		{Kind: "landscape", Location: "notes.txt"},
		{Kind: "keyart", Location: "keyart.jpg"},
		{Kind: "poster", Location: "https://img.example/second.jpg"},
		{Kind: "poster", Location: "second-poster.jpg"},
	}}
	in.art[planSeries] = ingestArt{dir: &series, entries: []domain.NFOArtwork{
		{Kind: "poster", Location: "https://img.example/series.jpg"},
		{Kind: "poster", Location: "season02-nfo.jpg", Season: &two},
		{Kind: "banner", Location: "https://img.example/s3.jpg", Season: new(int)},
	}}
	in.art[planEp] = ingestArt{dir: &ingestDir{planRoot, "Show/Season 02"}, entries: []domain.NFOArtwork{
		{Kind: "poster", Location: "thumbs/e01.jpg"}, {Kind: "thumb", Location: "https://img.example/e01.jpg"},
	}}
	// An NFO whose source is gone keeps only its URLs.
	in.art[planOther] = ingestArt{entries: []domain.NFOArtwork{{Kind: "poster", Location: "poster.jpg"}, {Kind: "banner", Location: "https://img.example/b.jpg"}}}
	for _, p := range []string{"Inception (2010)/Inception (2010)-poster.png", "Inception (2010)/extrafanart/fanart 2.jpg", "Inception (2010)/extrafanart/fanart3.jpg",
		"Shared/fanart.jpg", "Inception (2010)/second-poster.jpg", "Show/season02-nfo.jpg", "Show/Season 02/thumbs/e01.jpg"} {
		in.nfoFiles[ingestDir{planRoot, p}] = ingestFile{path: p, size: 7, modified: 9}
	}
	refs := nfoReferences(in)
	for _, ref := range refs {
		if strings.Contains(ref.dir, "..") || strings.HasPrefix(ref.dir, "/") {
			t.Fatal("unsafe reference listed", ref)
		}
	}
	plan := planItemImages(in)
	expectPlan(t, "movie art", plan[planMovie],
		"Primary/0/nfo=Inception (2010)/Inception (2010)-poster.png", "Primary/0/remote=https://img.example/second.jpg",
		"Backdrop/0/remote=https://img.example/f0.jpg", "Backdrop/1/nfo=Inception (2010)/extrafanart/fanart 2.jpg",
		"Backdrop/2/nfo=Inception (2010)/extrafanart/fanart3.jpg", "Backdrop/3/nfo=Shared/fanart.jpg")
	expectPlan(t, "series art", plan[planSeries], "Primary/0/remote=https://img.example/series.jpg")
	expectPlan(t, "season art", plan[planSeason], "Primary/0/nfo=Show/season02-nfo.jpg")
	expectPlan(t, "episode art", plan[planEp], "Thumb/0/nfo=Show/Season 02/thumbs/e01.jpg", "Thumb/0/remote=https://img.example/e01.jpg")
	expectPlan(t, "orphaned art", plan[planOther], "Banner/0/remote=https://img.example/b.jpg")
}

func TestIngestPlanHelpers(t *testing.T) {
	if decodeArtFact([]byte(`{"kind":"poster"}`)) != nil || decodeArtFact([]byte(`[{"kind":"","location":"x"}]`)) != nil {
		t.Fatal("invalid art fact decoded")
	}
	if got := decodeArtFact([]byte(`[{"kind":"poster","location":"p.jpg","season":1}]`)); len(got) != 1 || *got[0].Season != 1 {
		t.Fatal("art fact", got)
	}
	if parentDirectory("a.mkv") != "" || parentDirectory("x/y/a.mkv") != "x/y" || registeredDirectory(".") != "" || registeredDirectory("x") != "x" {
		t.Fatal("directories")
	}
	if videoBase("x/Film (2000).mkv") != "Film (2000)" {
		t.Fatal("video base")
	}
	slots := []ingestSlot{{"Primary", 0, "remote"}, {"Backdrop", 1, "local"}, {"Primary", 0, "local"}, {"Backdrop", 0, "nfo"}}
	slices.SortFunc(slots, compareIngestSlots)
	if fmt.Sprint(slots) != "[{Backdrop 0 nfo} {Backdrop 1 local} {Primary 0 local} {Primary 0 remote}]" {
		t.Fatal("slot order", slots)
	}
	for kind, want := range map[string]string{"poster": "Primary", "FANART": "Backdrop", "backdrop": "Backdrop", "banner": "Banner", "clearart": "ClearArt",
		"clearlogo": "ClearLogo", "logo": "Logo", "landscape": "Landscape", "thumb": "Thumb", "disc": "Disc", "discart": "Disc", "keyart": ""} {
		if got := nfoArtType(kind, "Movie"); got != want {
			t.Fatal(kind, got)
		}
	}
	old := ingestRow{root: planRoot, path: "a.jpg", modified: new(int64), size: new(int64)}
	if !sameIngestSource(old, domain.ItemImageInput{RootID: planRoot, RelativePath: "a.jpg", SourceModifiedUnixNano: new(int64), SourceSize: new(int64)}) ||
		sameIngestSource(old, domain.ItemImageInput{RootID: planRoot, RelativePath: "a.jpg"}) {
		t.Fatal("source comparison")
	}
}
