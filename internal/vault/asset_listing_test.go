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

// Duplicate name+version rows in a hand-edited manifest must not inflate
// counts anywhere: list, show, and GetVersionList have to agree.
func TestDuplicateManifestRowsCountOnce(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	// Append a duplicate of an existing row plus a second distinct version.
	dup := `
[[assets]]
name = "open-source-preparation"
version = "1.2.0"
type = "skill"

[assets.source-path]
path = "skills/open-source-preparation"

[[assets]]
name = "open-source-preparation"
version = "1.3.0"
type = "skill"

[assets.source-path]
path = "skills/open-source-preparation"
`
	f, err := os.OpenFile(filepath.Join(dir, "sx.toml"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(dup); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	list, err := v.ListAssets(ctx, ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	var summary AssetSummary
	for _, a := range list.Assets {
		if a.Name == "open-source-preparation" {
			summary = a
		}
	}
	if summary.VersionsCount != 2 || summary.LatestVersion != "1.3.0" {
		t.Errorf("summary = %+v, want 2 versions with latest 1.3.0", summary)
	}

	details, err := v.GetAssetDetails(ctx, "open-source-preparation")
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if len(details.Versions) != summary.VersionsCount {
		t.Errorf("show has %d versions, list says %d — they must agree", len(details.Versions), summary.VersionsCount)
	}

	versions, err := v.GetVersionList(ctx, "open-source-preparation")
	if err != nil {
		t.Fatalf("GetVersionList: %v", err)
	}
	if strings.Join(versions, ",") != "1.2.0,1.3.0" {
		t.Errorf("GetVersionList = %v, want deduped [1.2.0 1.3.0]", versions)
	}
}

// A hand-authored row that omits type but whose source metadata.toml
// declares one must be typed (and type-filterable) in listings.
func TestManifestRowWithoutTypeUsesSourceMetadata(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	m, _, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Assets {
		if m.Assets[i].Name == "open-source-preparation" {
			m.Assets[i].Type = asset.Type{}
		}
	}
	if err := manifest.Save(dir, m); err != nil {
		t.Fatal(err)
	}

	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := v.ListAssets(context.Background(), ListAssetsOptions{Type: "skill"})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	if len(list.Assets) != 1 || list.Assets[0].Name != "open-source-preparation" {
		t.Fatalf("filtered ListAssets = %+v, want open-source-preparation via metadata.toml type", list.Assets)
	}
	if list.Assets[0].Type.Key != "skill" {
		t.Errorf("type = %q, want skill from source metadata.toml", list.Assets[0].Type.Key)
	}
}

// Discovery must not follow source paths out of the vault root (absolute,
// tilde, or ../ escapes): the listing degrades to manifest-only data
// instead of reading arbitrary local files.
func TestDiscoverySkipsSourcePathsOutsideVaultRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "metadata.toml"),
		[]byte("[asset]\nname = \"evil\"\nversion = \"1.0.0\"\ntype = \"skill\"\ndescription = \"secret local data\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	m := &manifest.Manifest{
		SchemaVersion: 2,
		Assets: []manifest.Asset{
			{Name: "abs", Version: "1.0.0", Type: asset.TypeSkill,
				SourcePath: &manifest.SourcePath{Path: outside}},
			{Name: "escape", Version: "1.0.0", Type: asset.TypeSkill,
				SourcePath: &manifest.SourcePath{Path: "../" + filepath.Base(outside)}},
			{Name: "tilde", Version: "1.0.0", Type: asset.TypeSkill,
				SourcePath: &manifest.SourcePath{Path: "~/secrets"}},
		},
	}
	// A committed symlink inside the vault pointing outside it: the
	// vault-relative path looks contained, but resolution must not follow
	// it out of the root.
	symlinkOK := os.Symlink(outside, filepath.Join(dir, "wip")) == nil
	if symlinkOK {
		m.Assets = append(m.Assets, manifest.Asset{
			Name: "symlink", Version: "1.0.0", Type: asset.TypeSkill,
			SourcePath: &manifest.SourcePath{Path: "wip"},
		})
	}
	if err := manifest.Save(dir, m); err != nil {
		t.Fatal(err)
	}

	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := v.ListAssets(ctx, ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	wantAssets := 3
	if symlinkOK {
		wantAssets = 4
	}
	if len(list.Assets) != wantAssets {
		t.Fatalf("ListAssets = %+v, want all %d assets (manifest-only data)", list.Assets, wantAssets)
	}
	for _, a := range list.Assets {
		if a.Description != "" {
			t.Errorf("asset %q description = %q, must not be read from outside the vault", a.Name, a.Description)
		}
	}
	names := []string{"abs"}
	if symlinkOK {
		names = append(names, "symlink")
	}
	for _, name := range names {
		details, err := v.GetAssetDetails(ctx, name)
		if err != nil {
			t.Fatalf("GetAssetDetails(%s): %v", name, err)
		}
		if details.Description != "" || details.Versions[0].FilesCount != 0 {
			t.Errorf("details for %q = %+v, must not expose files outside the vault", name, details)
		}
	}
}

// A manifest row can declare a newer version than the stored list.txt
// knows (hand-edited install-in-place vaults). Install resolves it, so
// list/show must surface it too.
func TestNewerManifestOnlyVersionOfStoredAsset(t *testing.T) {
	dir := t.TempDir()
	v := seedV2PathVault(t, dir)

	// chat has stored versions 1.0 and 2.0; declare a 3.0 living outside
	// assets/.
	if err := os.MkdirAll(filepath.Join(dir, "wip", "chat"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wip", "chat", "SKILL.md"), []byte("# chat 3.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m, _, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Assets = append(m.Assets, manifest.Asset{
		Name: "chat", Version: "3.0", Type: asset.TypeSkill,
		SourcePath: &manifest.SourcePath{Path: "wip/chat"},
	})
	if err := manifest.Save(dir, m); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	list, err := v.ListAssets(ctx, ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	var chat AssetSummary
	for _, a := range list.Assets {
		if a.Name == "chat" {
			chat = a
		}
	}
	if chat.LatestVersion != "3.0" || chat.VersionsCount != 3 {
		t.Errorf("chat summary = %+v, want latest 3.0 of 3 versions", chat)
	}

	details, err := v.GetAssetDetails(ctx, "chat")
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if len(details.Versions) != 3 || details.Versions[2].Version != "3.0" {
		t.Errorf("versions = %+v, want [1.0 2.0 3.0]", details.Versions)
	}
	if details.Versions[2].FilesCount != 1 {
		t.Errorf("3.0 FilesCount = %d, want 1 (SKILL.md from wip/chat)", details.Versions[2].FilesCount)
	}
}

// A namespaced asset's top-level entry under assets/ is only a namespace
// directory, so it reaches the manifest merge — which must still surface
// its stored versions and content, agreeing with vault show.
func TestListAssetsNamespacedAssetUsesStoredVersions(t *testing.T) {
	dir := t.TempDir()
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
	// v2 layout: materialized root view + two archived versions.
	write("assets/opsx/apply/SKILL.md", "# apply 1.1\n")
	for _, v := range []string{"1.0", "1.1"} {
		write(".sx/versions/opsx/apply/"+v+"/SKILL.md", "# apply "+v+"\n")
		write(".sx/versions/opsx/apply/"+v+"/metadata.toml",
			"[asset]\nname = \"opsx/apply\"\nversion = \""+v+"\"\ntype = \"skill\"\ndescription = \"Apply things\"\n")
	}
	write(".sx/versions/opsx/apply/list.txt", "1.0\n1.1\n")
	m := &manifest.Manifest{
		SchemaVersion: 2,
		Assets: []manifest.Asset{
			{Name: "opsx/apply", Version: "1.1", Type: asset.TypeSkill,
				SourcePath: &manifest.SourcePath{Path: ".sx/versions/opsx/apply/1.1"}},
		},
	}
	if err := manifest.Save(dir, m); err != nil {
		t.Fatal(err)
	}

	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	list, err := v.ListAssets(ctx, ListAssetsOptions{})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	var summary AssetSummary
	for _, a := range list.Assets {
		if a.Name == "opsx/apply" {
			summary = a
		}
	}
	if summary.Name == "" {
		t.Fatalf("opsx/apply missing from %+v", list.Assets)
	}
	if summary.VersionsCount != 2 || summary.LatestVersion != "1.1" {
		t.Errorf("summary = %+v, want 2 stored versions with latest 1.1", summary)
	}
	if summary.Type.Key != "skill" || summary.Description != "Apply things" {
		t.Errorf("summary = %+v, want type/description from stored metadata", summary)
	}

	details, err := v.GetAssetDetails(ctx, "opsx/apply")
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if len(details.Versions) != summary.VersionsCount {
		t.Errorf("show has %d versions, list says %d — they must agree", len(details.Versions), summary.VersionsCount)
	}
}

// Renaming a manifest-only asset must fail with a clear explanation, not a
// raw filesystem error; renaming an unknown asset says not found.
func TestRenameManifestOnlyAssetFailsClearly(t *testing.T) {
	dir := t.TempDir()
	seedSourcePathVault(t, dir)
	v, err := NewPathVault("file://" + dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	err = v.RenameAsset(ctx, "open-source-preparation", "osp")
	if err == nil || !strings.Contains(err.Error(), "no stored files") {
		t.Errorf("RenameAsset = %v, want a clear no-stored-files error", err)
	}
	err = v.RenameAsset(ctx, "nope", "nope2")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("RenameAsset(nope) = %v, want not-found error", err)
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
