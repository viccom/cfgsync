package handler

import (
	"sort"

	"github.com/viccom/cfgsync/internal/semver"
)

// sortReleasesBySemverDesc sorts the caller-supplied slice in place using
// semver §11 precedence (newest first). The versionAt closure returns the
// version string for element i.
//
// SQLite's ORDER BY version_pre is lexical, not semver — "rc.11" < "rc.2"
// textually but "rc.11" > "rc.2" semantically. We perform ordering in Go to
// keep prerelease ordering correct.
//
// Versions that fail to parse (impossible for rows inserted via the upload
// pipeline, which validates via manifest.ParseAndValidate) sort to the end
// with their relative order preserved by sort.SliceStable.
func sortReleasesBySemverDesc[T any](items []T, versionAt func(i int) string) {
	parsed := make([]semver.Parsed, len(items))
	for i := range items {
		p, err := semver.Parse(versionAt(i))
		if err != nil {
			// Sentinel: lower than any real semver. Still uses Compare
			// consistently so the sort is total.
			p = semver.Parsed{Major: -1, Minor: -1, Patch: -1, Pre: "zzz"}
		}
		parsed[i] = p
	}
	// Sort the index, not the items directly, so parsed[i] stays aligned
	// with the original item i. Then permute a copy of items into the
	// sorted order — copying first avoids read-while-write aliasing on the
	// destination slice.
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return parsed[idx[a]].Compare(parsed[idx[b]]) > 0
	})
	src := make([]T, len(items))
	copy(src, items)
	for i, j := range idx {
		items[i] = src[j]
	}
}

// pickLatestVersion returns the highest-precedence version from a flat list,
// or "" if the list is empty. Uses the same semver ordering as
// sortReleasesBySemverDesc.
func pickLatestVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	parsed := make([]semver.Parsed, len(versions))
	for i, v := range versions {
		p, err := semver.Parse(v)
		if err != nil {
			p = semver.Parsed{Major: -1, Minor: -1, Patch: -1, Pre: "zzz"}
		}
		parsed[i] = p
	}
	best := 0
	for i := 1; i < len(versions); i++ {
		if parsed[i].Compare(parsed[best]) > 0 {
			best = i
		}
	}
	return versions[best]
}
