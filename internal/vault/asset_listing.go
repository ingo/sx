package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sleuth-io/sx/v2/internal/manifest"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
	"github.com/sleuth-io/sx/v2/internal/vault/layout"
	"github.com/sleuth-io/sx/v2/internal/version"
)

// This file implements asset discovery (list/show) shared by the
// file-backed vaults (GitVault, PathVault). Discovery merges two sources:
//
//   - the stored assets/ tree (published layout, v1 or v2)
//   - manifest [[assets]] rows with no stored files — the install-in-place
//     model where a source-path row points at files elsewhere in the vault,
//     or a source-http/git row points outside it entirely
//
// Install resolution is manifest-driven (manifest.Resolve), so a listing
// that only scanned assets/ silently hid every asset the vault would in
// fact install (issue #228). Versions are the union of stored list.txt
// entries and manifest rows per name, so a manifest row for a version the
// storage doesn't know still surfaces.

// listFileVaultAssets implements ListAssets over a synced vault root.
func listFileVaultAssets(vaultRoot string, l layout.Layout, opts ListAssetsOptions) (*ListAssetsResult, error) {
	rowsByName, rowOrder, err := manifestAssetRows(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault manifest: %w", err)
	}
	assets, seen, err := storedAssetSummaries(vaultRoot, l, opts, rowsByName)
	if err != nil {
		return nil, err
	}
	assets = append(assets, manifestOnlyAssetSummaries(vaultRoot, seen, opts, rowsByName, rowOrder)...)
	// Stored entries arrive in directory order and manifest entries in
	// manifest order; sort the merged view so output stays stable.
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })

	if search := strings.TrimSpace(opts.Search); search != "" {
		assets = filterBySearch(assets, search)
	}
	if opts.Limit > 0 && len(assets) > opts.Limit {
		assets = assets[:opts.Limit]
	}
	return &ListAssetsResult{Assets: assets}, nil
}

// storedAssetSummaries scans the layout's assets/ tree. The returned set
// records every directory-backed name — including ones a type filter
// discarded — so the manifest merge never duplicates a stored asset.
func storedAssetSummaries(vaultRoot string, l layout.Layout, opts ListAssetsOptions, rowsByName map[string][]manifest.Asset) ([]AssetSummary, map[string]bool, error) {
	seen := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(vaultRoot, l.AssetsRoot()))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, seen, nil
		}
		return nil, nil, fmt.Errorf("failed to read assets directory: %w", err)
	}

	var assets []AssetSummary
	for _, entry := range filterScanEntries(entries) {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		stored, err := versionListForAsset(vaultRoot, l, name)
		if err != nil {
			continue // Skip if versions are unreadable
		}
		versions := unionVersions(stored, rowsByName[name])
		if len(versions) == 0 {
			continue // Skip if no versions
		}
		seen[name] = true

		latestVersion := versions[len(versions)-1]
		summary := AssetSummary{
			Name:          name,
			LatestVersion: latestVersion,
			VersionsCount: len(versions),
		}
		contentDir, hasContent := versionContentDir(vaultRoot, l, rowsByName[name], name, latestVersion)
		if hasContent {
			if meta := readSourceMetadata(contentDir); meta != nil {
				summary.Type = meta.Asset.Type
				summary.Description = meta.Asset.Description
			}
		}
		// The manifest is authoritative for install; fall back to its type
		// when no stored metadata declares one.
		if summary.Type.Key == "" {
			if row, ok := rowForVersion(rowsByName[name], latestVersion); ok {
				summary.Type = row.Type
			}
		}
		if info, _ := entry.Info(); info != nil {
			summary.CreatedAt = info.ModTime()
			summary.UpdatedAt = info.ModTime()
		}

		if opts.Type != "" && summary.Type.Key != opts.Type {
			continue
		}
		// AFTER the type filter: this fallback reads files, and a
		// filtered listing must not pay it for assets it discards.
		if summary.Description == "" && hasContent {
			// Assets published without a metadata description usually
			// still declare one in markdown frontmatter — show it.
			summary.Description = markdownDescription(contentDir)
		}
		assets = append(assets, summary)
	}
	return assets, seen, nil
}

// manifestOnlyAssetSummaries builds summaries for manifest [[assets]] rows
// whose names have no stored directory under assets/ — install-in-place
// source-path assets and http/git-sourced rows.
func manifestOnlyAssetSummaries(vaultRoot string, seen map[string]bool, opts ListAssetsOptions, rowsByName map[string][]manifest.Asset, rowOrder []string) []AssetSummary {
	var out []AssetSummary
	for _, name := range rowOrder {
		if seen[name] {
			continue
		}
		rows := rowsByName[name]
		versions := unionVersions(nil, rows)
		if len(versions) == 0 {
			continue
		}
		latest := versions[len(versions)-1]
		row := rows[0]
		if r, ok := rowForVersion(rows, latest); ok {
			row = r
		}

		summary := AssetSummary{
			Name:          name,
			Type:          row.Type,
			LatestVersion: latest,
			VersionsCount: len(versions),
		}
		contentDir, hasContent := sourcePathContentDir(vaultRoot, row.SourcePath)
		var meta *metadata.Metadata
		if summary.Type.Key == "" && hasContent {
			// A hand-authored row may omit type while its source
			// metadata.toml declares one; resolve before the filter so
			// --type doesn't hide the asset. Typed rows keep the
			// no-file-reads-before-filter fast path.
			if meta = readSourceMetadata(contentDir); meta != nil {
				summary.Type = meta.Asset.Type
			}
		}
		if opts.Type != "" && summary.Type.Key != opts.Type {
			continue
		}
		if hasContent {
			if meta == nil {
				meta = readSourceMetadata(contentDir)
			}
			if meta != nil {
				summary.Description = meta.Asset.Description
			}
			if summary.Description == "" {
				summary.Description = markdownDescription(contentDir)
			}
			if info, err := os.Stat(contentDir); err == nil {
				summary.CreatedAt = info.ModTime()
				summary.UpdatedAt = info.ModTime()
			}
		}
		out = append(out, summary)
	}
	return out
}

// fileVaultAssetDetails implements GetAssetDetails over a synced vault root.
// An asset is found if it has a stored directory under assets/ OR versions
// discoverable through the manifest.
func fileVaultAssetDetails(vaultRoot string, l layout.Layout, name string) (*AssetDetails, error) {
	rowsByName, _, err := manifestAssetRows(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault manifest: %w", err)
	}
	rows := rowsByName[name]

	assetDir := filepath.Join(vaultRoot, l.AssetDir(name))
	_, statErr := os.Stat(assetDir)
	dirExists := statErr == nil

	stored, err := versionListForAsset(vaultRoot, l, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get version list: %w", err)
	}
	versions := unionVersions(stored, rows)
	if len(versions) == 0 {
		if dirExists {
			return nil, fmt.Errorf("asset '%s' has no versions", name)
		}
		return nil, fmt.Errorf("asset '%s' not found", name)
	}

	var versionList []AssetVersion
	for _, v := range versions {
		entry := AssetVersion{Version: v}
		if dir, ok := versionContentDir(vaultRoot, l, rows, name, v); ok {
			if info, err := os.Stat(dir); err == nil {
				entry.CreatedAt = info.ModTime()
			}
			if entries, err := os.ReadDir(dir); err == nil {
				fileCount := 0
				for _, e := range entries {
					if !e.IsDir() && !utils.IsSyncArtifact(e.Name()) {
						fileCount++
					}
				}
				entry.FilesCount = fileCount
			}
		}
		versionList = append(versionList, entry)
	}

	latest := versions[len(versions)-1]
	details := &AssetDetails{
		Name:     name,
		Versions: versionList,
	}
	latestDir, hasLatest := versionContentDir(vaultRoot, l, rows, name, latest)
	if hasLatest {
		if meta := readSourceMetadata(latestDir); meta != nil {
			details.Type = meta.Asset.Type
			details.Description = meta.Asset.Description
			details.Metadata = meta
		}
		if details.Description == "" {
			details.Description = markdownDescription(latestDir)
		}
	}
	// The manifest is authoritative for install; fall back to its type when
	// no stored metadata declares one.
	if details.Type.Key == "" {
		if row, ok := rowForVersion(rows, latest); ok {
			details.Type = row.Type
		}
	}

	tsDir := assetDir
	if !dirExists && hasLatest {
		tsDir = latestDir
	}
	if info, err := os.Stat(tsDir); err == nil {
		details.CreatedAt = info.ModTime()
		details.UpdatedAt = info.ModTime()
	}
	return details, nil
}

// manifestAssetRows loads the manifest once and groups its asset rows by
// name, preserving first-appearance order. Rows with an empty version are
// dropped (they cannot be listed or resolved). A missing manifest yields an
// empty map; a corrupt one already fails detectLayout before discovery runs.
func manifestAssetRows(vaultRoot string) (map[string][]manifest.Asset, []string, error) {
	rows := map[string][]manifest.Asset{}
	m, ok, err := manifest.Load(vaultRoot)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return rows, nil, nil
	}
	var order []string
	for _, a := range m.Assets {
		if strings.TrimSpace(a.Version) == "" {
			continue
		}
		if _, dup := rows[a.Name]; !dup {
			order = append(order, a.Name)
		}
		rows[a.Name] = append(rows[a.Name], a)
	}
	return rows, order, nil
}

// unionVersions merges stored list.txt versions with the versions declared
// by an asset's manifest rows, semver-sorted and deduplicated. Install
// resolution is manifest-driven, so a manifest row's version must surface
// even when the stored list doesn't know it — and duplicate hand-edited
// rows must not inflate the count.
func unionVersions(stored []string, rows []manifest.Asset) []string {
	merged := append([]string(nil), stored...)
	for _, r := range rows {
		if strings.TrimSpace(r.Version) != "" {
			merged = append(merged, r.Version)
		}
	}
	return slices.Compact(version.Sort(merged))
}

// rowForVersion returns the first manifest row matching a version.
func rowForVersion(rows []manifest.Asset, v string) (manifest.Asset, bool) {
	for _, r := range rows {
		if r.Version == v {
			return r, true
		}
	}
	return manifest.Asset{}, false
}

// versionContentDir locates the on-disk files for one version of an asset:
// the stored archive when present, else the manifest row's source-path.
func versionContentDir(vaultRoot string, l layout.Layout, rows []manifest.Asset, name, v string) (string, bool) {
	stored := filepath.Join(vaultRoot, l.VersionDir(name, v))
	if info, err := os.Stat(stored); err == nil && info.IsDir() {
		return stored, true
	}
	if row, ok := rowForVersion(rows, v); ok {
		return sourcePathContentDir(vaultRoot, row.SourcePath)
	}
	return "", false
}

// readSourceMetadata parses dir/metadata.toml, returning nil when the file
// is absent or unparseable.
func readSourceMetadata(dir string) *metadata.Metadata {
	data, err := os.ReadFile(filepath.Join(dir, "metadata.toml"))
	if err != nil {
		return nil
	}
	meta, err := metadata.Parse(data)
	if err != nil {
		return nil
	}
	return meta
}

// sourcePathContentDir resolves a manifest source-path row to a readable
// content directory for discovery. Discovery is a read-only browse over a
// possibly third-party vault, so unlike install it refuses to follow the
// path outside the vault root: absolute and ~ paths, and relative paths
// escaping the root, return false and the listing degrades to manifest-only
// data. Zip-file sources and missing paths return false too.
func sourcePathContentDir(vaultRoot string, sp *manifest.SourcePath) (string, bool) {
	if sp == nil {
		return "", false
	}
	if filepath.IsAbs(sp.Path) || strings.HasPrefix(sp.Path, "~") {
		return "", false
	}
	root := filepath.Clean(vaultRoot)
	resolved := filepath.Clean(filepath.Join(root, filepath.FromSlash(sp.Path)))
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return resolved, true
}
