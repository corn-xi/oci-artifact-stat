// Package version decides which of a repository's tags names its highest
// published version.
//
// It works on tag strings alone, so it is the one part of the tool that is
// the same on every registry.
package version

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// floatingAliases are tags that move between releases and so never identify a
// version on their own.
var floatingAliases = []string{"latest", "stable", "dev", "main", "master", "edge", "nightly"}

// semverPrefix matches a leading, optionally v-prefixed dotted version.
//
// Two departures from the semver grammar, both forced by real registries: it
// is anchored only at the start, because tags carry qualifiers past the
// version ("2.18.0-rel-build42"); and it takes any number of components, not
// three, because truncating "2.11.1.2" collapses distinct releases into one.
// At least two components are required, so "1" is not a version.
var semverPrefix = regexp.MustCompile(`^v?([0-9]+(?:\.[0-9]+)+)(?:-([0-9A-Za-z.\-]+))?(?:\+([0-9A-Za-z.\-]+))?`)

// prereleaseMarker recognizes a suffix that marks an unreleased version.
//
// Semver cannot distinguish "3.0.0-rc1" from "2.18.0-rel-build42": both are
// X.Y.Z with identifiers. Treating every suffix as a prerelease would report
// whole registries as having no released version, so only a recognized marker
// counts; everything else is a build qualifier and is ignored.
var prereleaseMarker = regexp.MustCompile(`(?i)^(alpha|beta|rc|pre|preview|snapshot|dev|nightly|canary|eap|milestone|m[0-9]+)([.\-]?[0-9]+)?$`)

// Key is a tag's comparable version.
type Key struct {
	// Nums are the numeric components, padded to at least three so "1.2" and
	// "1.2.0" match, and kept as long as the tag so "2.11.1.2" outranks
	// "2.11.1".
	Nums []int
	// Prerelease holds the identifiers of a recognized prerelease suffix,
	// empty for a release. Build metadata is parsed off and discarded.
	Prerelease []string
}

// IsPrerelease reports whether the tag named an unreleased version.
func (k Key) IsPrerelease() bool { return len(k.Prerelease) > 0 }

// String renders the normalized version. The prerelease is kept, since
// dropping it would claim a release that does not exist.
func (k Key) String() string {
	parts := make([]string, 0, len(k.Nums))
	for _, n := range k.Nums {
		parts = append(parts, strconv.Itoa(n))
	}
	s := strings.Join(parts, ".")
	if k.IsPrerelease() {
		s += "-" + strings.Join(k.Prerelease, ".")
	}
	return s
}

// Compare returns -1, 0 or 1 ordering k against o: numeric components, then
// release above prerelease, then prerelease identifiers per the semver spec.
// Build qualifiers take no part, so "2.25.0-rel" and "2.25.0-rel-build42" are
// equal -- they are the same release.
func (k Key) Compare(o Key) int {
	for i := 0; i < len(k.Nums) || i < len(o.Nums); i++ {
		if a, b := at(k.Nums, i), at(o.Nums, i); a != b {
			return sign(a - b)
		}
	}
	switch {
	case !k.IsPrerelease() && o.IsPrerelease():
		return 1
	case k.IsPrerelease() && !o.IsPrerelease():
		return -1
	case !k.IsPrerelease() && !o.IsPrerelease():
		return 0
	}
	return comparePrerelease(k.Prerelease, o.Prerelease)
}

// Less reports whether k sorts below o.
func (k Key) Less(o Key) bool { return k.Compare(o) < 0 }

// comparePrerelease implements semver precedence: numeric identifiers compare
// numerically and rank below alphanumeric ones, and a longer list wins when
// all preceding fields are equal.
func comparePrerelease(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aNum := strconv.Atoi(a[i])
		bn, bNum := strconv.Atoi(b[i])
		switch {
		case aNum == nil && bNum == nil:
			if an != bn {
				return sign(an - bn)
			}
		case aNum == nil:
			return -1
		case bNum == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(a) - len(b))
}

// at reads a component, treating anything past the end as zero.
func at(nums []int, i int) int {
	if i < len(nums) {
		return nums[i]
	}
	return 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Parse extracts the version from a tag, reporting whether the tag is
// version-shaped at all.
func Parse(tag string) (Key, bool) {
	m := semverPrefix.FindStringSubmatch(tag)
	if m == nil {
		return Key{}, false
	}

	fields := strings.Split(m[1], ".")
	nums := make([]int, 0, max(len(fields), 3))
	for _, raw := range fields {
		n, err := strconv.Atoi(raw)
		if err != nil {
			// Out of range: not a version anyone published on purpose.
			return Key{}, false
		}
		nums = append(nums, n)
	}
	for len(nums) < 3 {
		nums = append(nums, 0)
	}

	key := Key{Nums: nums}
	if suffix := m[2]; suffix != "" {
		if ids := strings.Split(suffix, "."); prereleaseMarker.MatchString(ids[0]) {
			key.Prerelease = ids
		}
	}
	return key, true
}

// Friendly renders a tag for display: the normalized version when the tag is
// version-shaped, otherwise the tag unchanged.
func Friendly(tag string) string {
	if k, ok := Parse(tag); ok {
		return k.String()
	}
	return tag
}

// Better reports whether a candidate should displace the current best: higher
// version first, then the shorter tag, since the canonical tag is almost
// always shorter than its qualified twin. A tie keeps the incumbent, which
// makes selection stable for a given input order.
func Better(candKey Key, candTag string, bestKey Key, bestTag string) bool {
	if c := candKey.Compare(bestKey); c != 0 {
		return c > 0
	}
	return utf8.RuneCountInString(candTag) < utf8.RuneCountInString(bestTag)
}

// PickTag chooses the tag best representing a repository's highest published
// version. Push order is deliberately not consulted: a stale or un-bumped tag
// is very often the most recently pushed artifact.
//
// Prereleases are excluded unless asked for, since "the latest version"
// normally means the latest released one; a repository that has published
// nothing else falls back to them rather than reporting nothing. Failing any
// version-shaped tag: the first non-floating tag, then the first tag.
func PickTag(tags []string, includePrereleases bool) string {
	if tag := pickVersioned(tags, includePrereleases); tag != "" {
		return tag
	}
	if !includePrereleases {
		if tag := pickVersioned(tags, true); tag != "" {
			return tag
		}
	}
	for _, tag := range tags {
		if !isFloating(tag) {
			return tag
		}
	}
	if len(tags) > 0 {
		return tags[0]
	}
	return ""
}

func pickVersioned(tags []string, includePrereleases bool) string {
	best := -1
	var bestKey Key
	for i, tag := range tags {
		k, ok := Parse(tag)
		if !ok || (k.IsPrerelease() && !includePrereleases) {
			continue
		}
		if best < 0 || Better(k, tag, bestKey, tags[best]) {
			best, bestKey = i, k
		}
	}
	if best < 0 {
		return ""
	}
	return tags[best]
}

// isFloating reports whether a tag is one of the known moving aliases.
func isFloating(tag string) bool {
	for _, alias := range floatingAliases {
		if tag == alias {
			return true
		}
	}
	return false
}
