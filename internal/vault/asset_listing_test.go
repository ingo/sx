package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/manifest"
	"github.com/sleuth-io/sx/v2/internal/mgmt"
)

// seedSourcePathVault builds a hand-authored install-in-place vault (the
// issue #228 layout): a schema_version = 2 manifest whose [[assets]] rows
// point via source-path at files outside assets/ — no assets/ directory at
// all.
func seedSourcePathVault(t *testing.T, dir string) {
	t.Helper()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// One asset with a metadata.toml description, one relying on markdown
	// frontmatter only.
	write("skills/open-source-preparation/SKILL.md", "# open-source-preparation\n\nBody.\n")
	write("skills/open-source-preparation/metadata.toml",
		"[asset]\nname = \"open-source-preparation\"\nversion = \"1.2.0\"\ntype = \"skill\"\ndescription = \"Prepare a repo for open sourcing\"\n")
	write("rules/style-rules/RULE.md", "---\ndescription: House style rules\n---\n# style-rules\n")

	m := &manifest.Manifest{
		SchemaVersion: 2,
		Assets: []manifest.Asset{
			{
				Name: "open-source-preparation", Version: "1.2.0", Type: asset.TypeSkill,
				SourcePath: &manifest.SourcePath{Path: "skills/open-source-preparation"},
			},
			{
				Name: "style-rules", Version: "2.1.0", Type: asset.TypeRule,
				SourcePath: &manifest.SourcePath{Path: "rules/style-rules"},
			},
		},
	}
	if err := manifest.Save(dir, m); err != nil {
		t.Fatal(err)
	}
}

func TestListAssetsSourcePathManifestVault(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}

	list, err := v.ListAssets(context.Background(), ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(list.Assets) != 2 {
		t.Fatalf("ListAssets = %+v, want 2 assets", list.Assets)
	}
	byName := map[string]AssetSummary{}
	for _, a := range list.Assets {
		byName[a.Name] = a
	}

	skill, ok := byName["open-source-preparation"]
	if !ok {
		t.Fatalf("open-source-preparation missing from %+v", list.Assets)
	}
	if skill.Type.Key != "skill" || skill.LatestVersion != "1.2.0" || skill.VersionsCount != 1 {
		t.Errorf("skill summary = %+v", skill)
	}
	if skill.Description != "Prepare a repo for open sourcing" {
		t.Errorf("skill description = %q, want the metadata.toml description", skill.Description)
	}
	if skill.UpdatedAt.IsZero() {
		t.Error("skill timestamps should come from the source directory")
	}

	rule, ok := byName["style-rules"]
	if !ok {
		t.Fatalf("style-rules missing from %+v", list.Assets)
	}
	if rule.Type.Key != "rule" || rule.LatestVersion != "2.1.0" {
		t.Errorf("rule summary = %+v", rule)
	}
	if rule.Description != "House style rules" {
		t.Errorf("rule description = %q, want the frontmatter description", rule.Description)
	}
}

func TestListAssetsSourcePathManifestTypeFilter(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}

	list, err := v.ListAssets(context.Background(), ListAssetsOptions{Type: "skill"})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(list.Assets) != 1 || list.Assets[0].Name != "open-source-preparation" {
		t.Errorf("filtered ListAssets = %+v, want only open-source-preparation", list.Assets)
	}
}

func TestGetAssetDetailsSourcePathManifestVault(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	details, err := v.GetAssetDetails(ctx, "open-source-preparation")
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if details.Type.Key != "skill" || details.Description != "Prepare a repo for open sourcing" {
		t.Errorf("details = %+v", details)
	}
	if len(details.Versions) != 1 || details.Versions[0].Version != "1.2.0" {
		t.Fatalf("versions = %+v, want [1.2.0]", details.Versions)
	}
	if details.Versions[0].FilesCount != 2 {
		t.Errorf("FilesCount = %d, want 2 (SKILL.md + metadata.toml)", details.Versions[0].FilesCount)
	}
	if details.Metadata == nil {
		t.Error("Metadata should be parsed from the source directory")
	}

	// A rule with no metadata.toml still resolves type (manifest) and
	// description (frontmatter).
	details, err = v.GetAssetDetails(ctx, "style-rules")
	if err != nil {
		t.Fatalf("GetAssetDetails(style-rules): %v", err)
	}
	if details.Type.Key != "rule" {
		t.Errorf("style-rules type = %q, want manifest type \"rule\"", details.Type.Key)
	}
	if details.Description != "House style rules" {
		t.Errorf("style-rules description = %q", details.Description)
	}

	// Unknown assets still error clearly.
	if _, err := v.GetAssetDetails(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("GetAssetDetails(nope) = %v, want not-found error", err)
	}
}

// The end-to-end repro from issue #228: a git vault whose manifest declares
// source-path assets outside assets/ must list and show them.
func TestGitVaultSourcePathManifestDiscovery(t *testing.T) {
	mgmt.ResetActorCache()
	t.Setenv("SX_CACHE_DIR", t.TempDir())

	remoteDir := filepath.Join(t.TempDir(), "vault.git")
	gitRun(t, "", "init", "--bare", "-b", "main", remoteDir)

	writerDir := filepath.Join(t.TempDir(), "writer")
	gitRun(t, "", "init", "-b", "main", writerDir)
	gitRun(t, writerDir, "config", "user.email", "writer@example.com")
	gitRun(t, writerDir, "config", "user.name", "Writer")
	seedSourcePathVault(t, writerDir)
	gitRun(t, writerDir, "add", ".")
	gitRun(t, writerDir, "commit", "-m", "seed")
	gitRun(t, writerDir, "remote", "add", "origin", remoteDir)
	gitRun(t, writerDir, "push", "origin", "main")

	v, err := NewGitVault("file://" + remoteDir)
	if err != nil {
		t.Fatalf("NewGitVault: %v", err)
	}
	ctx := context.Background()

	list, err := v.ListAssets(ctx, ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	var names []string
	for _, a := range list.Assets {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "open-source-preparation,style-rules" {
		t.Fatalf("ListAssets = %v, want the two manifest assets", names)
	}

	details, err := v.GetAssetDetails(ctx, "open-source-preparation")
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if details.Type.Key != "skill" || len(details.Versions) != 1 {
		t.Errorf("details = %+v", details)
	}
}
