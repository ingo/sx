package vault

import (
	"fmt"
	"os"
	"path/filepath"
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
// fact install (issue #228).

// listFileVaultAssets implements ListAssets over a synced vault root.
func listFileVaultAssets(vaultRoot string, l layout.Layout, opts ListAssetsOptions) (*ListAssetsResult, error) {
	assets, seen, err := storedAssetSummaries(vaultRoot, l, opts)
	if err != nil {
		return nil, err
	}
	manifestOnly, err := manifestOnlyAssetSummaries(vaultRoot, seen, opts)
	if err != nil {
		return nil, err
	}
	assets = append(assets, manifestOnly...)
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
func storedAssetSummaries(vaultRoot string, l layout.Layout, opts ListAssetsOptions) ([]AssetSummary, map[string]bool, error) {
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
		versions, err := versionListForAsset(vaultRoot, l, entry.Name())
		if err != nil || len(versions) == 0 {
			continue // Skip if no versions
		}
		seen[entry.Name()] = true

		latestVersion := versions[len(versions)-1]
		summary := AssetSummary{
			Name:          entry.Name(),
			LatestVersion: latestVersion,
			VersionsCount: len(versions),
		}
		if metaData, err := os.ReadFile(filepath.Join(vaultRoot, l.MetadataPath(entry.Name(), latestVersion))); err == nil {
			if meta, err := metadata.Parse(metaData); err == nil {
				summary.Type = meta.Asset.Type
				summary.Description = meta.Asset.Description
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
		if summary.Description == "" {
			// Assets published without a metadata description usually
			// still declare one in markdown frontmatter — show it.
			summary.Description = markdownDescription(
				filepath.Join(vaultRoot, l.VersionDir(entry.Name(), latestVersion)))
		}
		assets = append(assets, summary)
	}
	return assets, seen, nil
}

// manifestOnlyAssetSummaries builds summaries for manifest [[assets]] rows
// whose names have no stored directory under assets/ — install-in-place
// source-path assets and http/git-sourced rows.
func manifestOnlyAssetSummaries(vaultRoot string, seen map[string]bool, opts ListAssetsOptions) ([]AssetSummary, error) {
	m, ok, err := manifest.Load(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault manifest: %w", err)
	}
	if !ok {
		return nil, nil
	}

	grouped := map[string][]manifest.Asset{}
	var order []string
	for _, a := range m.Assets {
		if seen[a.Name] || strings.TrimSpace(a.Version) == "" {
			continue
		}
		if _, dup := grouped[a.Name]; !dup {
			order = append(order, a.Name)
		}
		grouped[a.Name] = append(grouped[a.Name], a)
	}

	var out []AssetSummary
	for _, name := range order {
		rows := grouped[name]
		versions := make([]string, 0, len(rows))
		for _, r := range rows {
			versions = append(versions, r.Version)
		}
		versions = version.Sort(versions)
		latest := versions[len(versions)-1]
		row := rows[0]
		for _, r := range rows {
			if r.Version == latest {
				row = r
				break
			}
		}

		summary := AssetSummary{
			Name:          name,
			Type:          row.Type,
			LatestVersion: latest,
			VersionsCount: len(versions),
		}
		if opts.Type != "" && summary.Type.Key != opts.Type {
			continue
		}
		// File reads only after the type filter, mirroring the stored scan.
		if dir, ok := sourcePathContentDir(vaultRoot, row.SourcePath); ok {
			if metaData, err := os.ReadFile(filepath.Join(dir, "metadata.toml")); err == nil {
				if meta, err := metadata.Parse(metaData); err == nil {
					summary.Description = meta.Asset.Description
				}
			}
			if summary.Description == "" {
				summary.Description = markdownDescription(dir)
			}
			if info, err := os.Stat(dir); err == nil {
				summary.CreatedAt = info.ModTime()
				summary.UpdatedAt = info.ModTime()
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

// fileVaultAssetDetails implements GetAssetDetails over a synced vault root.
// An asset is found if it has a stored directory under assets/ OR versions
// discoverable through the manifest (versionListForAsset falls back there).
func fileVaultAssetDetails(vaultRoot string, l layout.Layout, name string) (*AssetDetails, error) {
	assetDir := filepath.Join(vaultRoot, l.AssetDir(name))
	_, statErr := os.Stat(assetDir)
	dirExists := statErr == nil

	versions, err := versionListForAsset(vaultRoot, l, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get version list: %w", err)
	}
	if len(versions) == 0 {
		if dirExists {
			return nil, fmt.Errorf("asset '%s' has no versions", name)
		}
		return nil, fmt.Errorf("asset '%s' not found", name)
	}

	rowsByVersion := manifestRowsForAsset(vaultRoot, name)
	// contentDir locates the on-disk files for one version: the stored
	// archive when present, else the manifest row's source-path.
	contentDir := func(v string) (string, bool) {
		stored := filepath.Join(vaultRoot, l.VersionDir(name, v))
		if info, err := os.Stat(stored); err == nil && info.IsDir() {
			return stored, true
		}
		if row, ok := rowsByVersion[v]; ok {
			return sourcePathContentDir(vaultRoot, row.SourcePath)
		}
		return "", false
	}

	var versionList []AssetVersion
	for _, v := range versions {
		entry := AssetVersion{Version: v}
		if dir, ok := contentDir(v); ok {
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
	if dir, ok := contentDir(latest); ok {
		if metaData, err := os.ReadFile(filepath.Join(dir, "metadata.toml")); err == nil {
			if meta, err := metadata.Parse(metaData); err == nil {
				details.Type = meta.Asset.Type
				details.Description = meta.Asset.Description
				details.Metadata = meta
			}
		}
		if details.Description == "" {
			details.Description = markdownDescription(dir)
		}
	}
	// The manifest is authoritative for install; fall back to its type when
	// no stored metadata declares one.
	if details.Type.Key == "" {
		if row, ok := rowsByVersion[latest]; ok {
			details.Type = row.Type
		}
	}

	tsDir := assetDir
	if !dirExists {
		if dir, ok := contentDir(latest); ok {
			tsDir = dir
		}
	}
	if info, err := os.Stat(tsDir); err == nil {
		details.CreatedAt = info.ModTime()
		details.UpdatedAt = info.ModTime()
	}
	return details, nil
}

// manifestRowsForAsset returns the manifest [[assets]] rows for name keyed
// by version. Missing or unreadable manifests yield an empty map — stored
// discovery works without one, and a corrupt manifest already fails
// detectLayout before discovery runs.
func manifestRowsForAsset(vaultRoot, name string) map[string]manifest.Asset {
	rows := map[string]manifest.Asset{}
	m, ok, err := manifest.Load(vaultRoot)
	if err != nil || !ok {
		return rows
	}
	for _, a := range m.Assets {
		if a.Name == name && strings.TrimSpace(a.Version) != "" {
			rows[a.Version] = a
		}
	}
	return rows
}

// sourcePathContentDir resolves a manifest source-path row to a readable
// content directory. Zip-file sources and unresolvable paths return false —
// discovery then falls back to manifest-only data.
func sourcePathContentDir(vaultRoot string, sp *manifest.SourcePath) (string, bool) {
	if sp == nil {
		return "", false
	}
	resolved, err := NewPathSourceHandler(vaultRoot).ResolvePath(sp.Path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return resolved, true
}
