package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type siteRepositoryFake struct {
	AccountRepository
	appearance domain.SiteAppearanceRecord
	plugins    domain.SitePluginsRecord
	hosts      []string
	hostsErr   error
	writes     []string
	adminReads []bool
}

func (f *siteRepositoryFake) GetSiteAppearance(_ context.Context, _ domain.Actor, admin bool) (domain.SiteAppearanceRecord, error) {
	f.adminReads = append(f.adminReads, admin)
	return f.appearance, nil
}

func (f *siteRepositoryFake) SetSiteAppearance(_ context.Context, _ domain.Actor, in domain.SiteAppearance, revision int64, source string) (domain.SiteAppearanceRecord, error) {
	f.writes = append(f.writes, "appearance:"+source)
	if revision != domain.AnyRevision && revision != f.appearance.Revision {
		return f.appearance, domain.ErrConflict
	}
	f.appearance = domain.SiteAppearanceRecord{SiteAppearance: in, Revision: f.appearance.Revision + 1}
	return f.appearance, nil
}

func (f *siteRepositoryFake) GetSitePlugins(_ context.Context, _ domain.Actor, admin bool) (domain.SitePluginsRecord, error) {
	f.adminReads = append(f.adminReads, admin)
	return f.plugins, nil
}

func (f *siteRepositoryFake) SetSitePlugins(_ context.Context, _ domain.Actor, in domain.SitePlugins, revision int64, source string) (domain.SitePluginsRecord, error) {
	f.writes = append(f.writes, "plugins:"+source)
	if revision != domain.AnyRevision && revision != f.plugins.Revision {
		return f.plugins, domain.ErrConflict
	}
	f.plugins = domain.SitePluginsRecord{SitePlugins: in, Revision: f.plugins.Revision + 1}
	return f.plugins, nil
}

func (f *siteRepositoryFake) GetSiteSettings(context.Context, domain.Actor) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	return f.appearance, f.plugins, nil
}

func (f *siteRepositoryFake) ImportSiteSettings(_ context.Context, _ domain.Actor, appearance domain.SiteAppearance, plugins domain.SitePlugins) (domain.SiteAppearanceRecord, domain.SitePluginsRecord, error) {
	f.writes = append(f.writes, "import")
	f.appearance = domain.SiteAppearanceRecord{SiteAppearance: appearance, Revision: f.appearance.Revision + 1}
	f.plugins = domain.SitePluginsRecord{SitePlugins: plugins, Revision: f.plugins.Revision + 1}
	return f.appearance, f.plugins, nil
}

func (f *siteRepositoryFake) SiteFontHosts(context.Context) ([]string, error) {
	return f.hosts, f.hostsErr
}

func newSiteRepositoryFake() *siteRepositoryFake {
	return &siteRepositoryFake{appearance: domain.SiteAppearanceRecord{SiteAppearance: domain.DefaultSiteAppearance()}, plugins: domain.SitePluginsRecord{SitePlugins: domain.DefaultSitePlugins()}}
}

func TestSiteAppearanceValidatesAndSanitizesBeforeStorage(t *testing.T) {
	repo := newSiteRepositoryFake()
	a := accountService(t, repo, accountPasswordFake{})
	ctx := context.Background()
	in := domain.DefaultSiteAppearance()
	in.CustomCSS = "a { color: red } b { background: url(https://evil.example/x) }"
	for _, bad := range []domain.SiteAppearance{
		{},
		func() domain.SiteAppearance { v := in; v.CustomCSS = "</style><script>alert(1)</script>"; return v }(),
		func() domain.SiteAppearance { v := in; v.FontHosts = []string{"evil.example; script-src *"}; return v }(),
	} {
		if _, err := a.SetSiteAppearance(ctx, accountTestActor(), bad, 0); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := a.SetSiteAppearance(ctx, accountTestActor(), in, -1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("negative revision: %v", err)
	}
	if _, err := a.SetSiteAppearance(ctx, domain.Actor{}, in, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid actor: %v", err)
	}
	if len(repo.writes) != 0 {
		t.Fatalf("invalid appearance reached storage: %v", repo.writes)
	}
	config, err := a.SetSiteAppearance(ctx, accountTestActor(), in, 0)
	if err != nil || config.Revision != 1 || len(config.CSSIssues) != 1 || config.CSSIssues[0].Code != domain.CSSIssueExternalURL {
		t.Fatalf("set: %+v %v", config, err)
	}
	// Text stored behind the application's back is sanitized again on read.
	repo.appearance.CustomCSS = "a { color: blue } @import url(https://evil.example/x.css);"
	view, err := a.SiteAppearance(ctx, accountTestActor())
	if err != nil || view.CSS != "a{color:blue}" {
		t.Fatalf("view: %+v %v", view, err)
	}
	if _, err := a.SiteAppearanceConfig(ctx, accountTestActor()); err != nil {
		t.Fatal(err)
	}
	if len(repo.adminReads) != 2 || repo.adminReads[0] || !repo.adminReads[1] {
		t.Fatalf("users must read without and administrators with the admin check: %v", repo.adminReads)
	}
	if _, err := a.SetSiteAppearance(ctx, accountTestActor(), in, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	reset, err := a.ResetSiteAppearance(ctx, accountTestActor())
	if err != nil || reset.CustomCSS != "" || reset.DefaultTheme != domain.ThemeSystem || repo.writes[len(repo.writes)-1] != "appearance:reset" {
		t.Fatalf("reset: %+v %v %v", reset, err, repo.writes)
	}
}

func TestSitePluginsViewHidesDisabledSettings(t *testing.T) {
	repo := newSiteRepositoryFake()
	a := accountService(t, repo, accountPasswordFake{})
	ctx := context.Background()
	in := domain.SitePlugins{Plugins: []domain.SitePluginState{{ID: "a.one", Enabled: false}, {ID: "a.two", Enabled: true}},
		Settings: map[string]json.RawMessage{"a.one": json.RawMessage(`{"secret":"x"}`), "a.two": json.RawMessage(`{ "k" : [1, 2] }`), "a.three": json.RawMessage(`{}`)}}
	stored, err := a.SetSitePlugins(ctx, accountTestActor(), in, 0)
	if err != nil || string(stored.Settings["a.two"]) != `{"k":[1,2]}` {
		t.Fatalf("set: %+v %v", stored, err)
	}
	view, err := a.SitePlugins(ctx, accountTestActor())
	if err != nil || len(view.Plugins) != 2 || view.Settings["a.one"] != nil || view.Settings["a.two"] == nil || view.Settings["a.three"] == nil {
		t.Fatalf("view: %+v %v", view, err)
	}
	if _, err := a.SetSitePlugins(ctx, accountTestActor(), domain.SitePlugins{Plugins: []domain.SitePluginState{{ID: "Bad"}}, Settings: map[string]json.RawMessage{}}, 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad id: %v", err)
	}
	if reset, err := a.ResetSitePlugins(ctx, accountTestActor()); err != nil || len(reset.Plugins) != 0 || len(reset.Settings) != 0 {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	if config, err := a.SitePluginsConfig(ctx, accountTestActor()); err != nil || config.Revision != 2 {
		t.Fatalf("config: %+v %v", config, err)
	}
}

func TestSiteSettingsExportImport(t *testing.T) {
	repo := newSiteRepositoryFake()
	a := accountService(t, repo, accountPasswordFake{})
	ctx := context.Background()
	repo.appearance.CustomCSS = "a{color:red}"
	repo.plugins.Settings = map[string]json.RawMessage{"a.b": json.RawMessage(`{"x":1}`)}
	now := time.Date(2026, 10, 4, 12, 0, 0, 999, time.FixedZone("x", 3600))
	document, err := a.ExportSiteSettings(ctx, accountTestActor(), now)
	if err != nil || document.Format != domain.SiteSettingsFormat || document.Version != 1 || document.ExportedAt == nil || !document.ExportedAt.Equal(now.Truncate(time.Second)) || document.ExportedAt.Location() != time.UTC {
		t.Fatalf("export: %+v %v", document, err)
	}
	for _, bad := range []domain.SiteSettingsDocument{
		{Format: "other", Version: 1, Appearance: document.Appearance, Plugins: document.Plugins},
		{Format: domain.SiteSettingsFormat, Version: 2, Appearance: document.Appearance, Plugins: document.Plugins},
		{Format: domain.SiteSettingsFormat, Version: 1, Appearance: domain.SiteAppearance{}, Plugins: document.Plugins},
		{Format: domain.SiteSettingsFormat, Version: 1, Appearance: document.Appearance, Plugins: domain.SitePlugins{}},
	} {
		if _, err := a.ImportSiteSettings(ctx, accountTestActor(), bad); err == nil {
			t.Fatalf("imported %+v", bad)
		}
	}
	if len(repo.writes) != 0 {
		t.Fatal("an invalid import reached storage")
	}
	result, err := a.ImportSiteSettings(ctx, accountTestActor(), document)
	if err != nil || result.Appearance.CustomCSS != "a{color:red}" || len(repo.writes) != 1 {
		t.Fatalf("import: %+v %v", result, err)
	}
}

func TestSiteFontHostsDropsAnythingButHostNames(t *testing.T) {
	repo := newSiteRepositoryFake()
	a := accountService(t, repo, accountPasswordFake{})
	repo.hosts = []string{"fonts.example.com", "evil.example; script-src *", "UPPER.example.com", "1.2.3.4", "cdn.example.net"}
	hosts, err := a.SiteFontHosts(context.Background())
	if err != nil || strings.Join(hosts, ",") != "fonts.example.com,cdn.example.net" {
		t.Fatalf("%v %v", hosts, err)
	}
	repo.hostsErr = domain.ErrDatabase
	if _, err := a.SiteFontHosts(context.Background()); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal(err)
	}
}
