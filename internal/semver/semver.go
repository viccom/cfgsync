// Package semver parses the subset of semver.org v2 that cfgsync accepts
// for app releases: MAJOR.MINOR.PATCH with an optional dash-separated
// pre-release marker. Build metadata is rejected — releases must be
// uniquely identified by their pre-release tag.
package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parsed is the decomposed form stored in app_releases so that ORDER BY
// can sort versions natively without re-parsing on every query.
type Parsed struct {
	Major int
	Minor int
	Patch int
	Pre   string // "" for a final release; otherwise e.g. "rc.1", "alpha.2"
}

// pattern mirrors semver.org v2 numeric identifiers plus the dotted
// alphanumeric pre-release tail. Numeric identifiers reject leading zeros
// per §9. Build metadata (+...) is intentionally rejected by omitting it
// from the pattern.
var pattern = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Parse decomposes a version string. Returns an error for malformed input
// or for versions carrying build metadata.
func Parse(v string) (Parsed, error) {
	m := pattern.FindStringSubmatch(v)
	if m == nil {
		return Parsed{}, fmt.Errorf("semver: invalid version %q", v)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return Parsed{Major: major, Minor: minor, Patch: patch, Pre: m[4]}, nil
}

// String renders the canonical "Major.Minor.Patch[-pre]" form.
func (p Parsed) String() string {
	if p.Pre == "" {
		return fmt.Sprintf("%d.%d.%d", p.Major, p.Minor, p.Patch)
	}
	return fmt.Sprintf("%d.%d.%d-%s", p.Major, p.Minor, p.Patch, p.Pre)
}

// Compare returns -1, 0, or +1 per semver.org precedence rules.
// Final releases rank higher than any pre-release of the same Major.Minor.Patch
// (so 1.0.0 > 1.0.0-rc.1).
func (p Parsed) Compare(o Parsed) int {
	if c := cmpInt(p.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(p.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(p.Patch, o.Patch); c != 0 {
		return c
	}
	return cmpPre(p.Pre, o.Pre)
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// cmpPre implements semver.org §11 (precedence of pre-release identifiers).
// A version without pre-release ranks higher than one with pre-release.
// Two pre-releases compare by splitting on "." and comparing each segment:
// numeric segments compare numerically; alphanumeric compare lexically;
// numeric segments rank lower than alphanumeric.
func cmpPre(a, b string) int {
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		aa, aErr := strconv.Atoi(as[i])
		bb, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if c := cmpInt(aa, bb); c != 0 {
				return c
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}
