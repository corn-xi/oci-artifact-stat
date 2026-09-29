package version

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		tag        string
		want       string // Key.String()
		prerelease bool
		ok         bool
	}{
		{name: "plain", tag: "1.2.3", want: "1.2.3", ok: true},
		{name: "v prefix", tag: "v1.2.3", want: "1.2.3", ok: true},
		{name: "missing patch defaults to zero", tag: "1.2", want: "1.2.0", ok: true},
		{name: "leading zeros normalized", tag: "01.02.03", want: "1.2.3", ok: true},
		// Four-part release trains are common in enterprises; truncating to
		// three collapses distinct releases into one.
		{name: "fourth component kept", tag: "1.2.3.4", want: "1.2.3.4", ok: true},
		{name: "five components", tag: "1.2.3.4.5", want: "1.2.3.4.5", ok: true},

		// The crux: a build qualifier is NOT a prerelease. Treating it as one
		// would make every component-qualified registry report as unreleased.
		{name: "build qualifier is not a prerelease", tag: "2.18.0-rel-build42", want: "2.18.0", ok: true},
		{name: "bare qualifier is not a prerelease", tag: "2.25.0-rel", want: "2.25.0", ok: true},
		{name: "build metadata ignored", tag: "1.2.3+build5", want: "1.2.3", ok: true},

		{name: "rc is a prerelease", tag: "3.0.0-rc1", want: "3.0.0-rc1", prerelease: true, ok: true},
		{name: "dotted prerelease", tag: "1.0.0-alpha.1", want: "1.0.0-alpha.1", prerelease: true, ok: true},
		{name: "beta with separator", tag: "1.0.0-beta.2", want: "1.0.0-beta.2", prerelease: true, ok: true},
		{name: "snapshot", tag: "4.1.0-SNAPSHOT", want: "4.1.0-SNAPSHOT", prerelease: true, ok: true},
		{name: "milestone", tag: "5.0.0-m2", want: "5.0.0-m2", prerelease: true, ok: true},
		{name: "prerelease with build metadata", tag: "2.0.0-rc.1+sha.abc", want: "2.0.0-rc.1", prerelease: true, ok: true},

		{name: "needs a dot", tag: "1", ok: false},
		{name: "not anchored mid-string", tag: "build-1.2.3", ok: false},
		{name: "non-numeric", tag: "custom-tag-meta-1-6-0", ok: false},
		{name: "floating alias", tag: "latest", ok: false},
		{name: "empty", tag: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.tag)
			if ok != tt.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v", tt.tag, ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.String() != tt.want {
				t.Errorf("Parse(%q) = %q, want %q", tt.tag, got.String(), tt.want)
			}
			if got.IsPrerelease() != tt.prerelease {
				t.Errorf("Parse(%q).IsPrerelease() = %v, want %v", tt.tag, got.IsPrerelease(), tt.prerelease)
			}
		})
	}
}

func TestKeyCompare(t *testing.T) {
	mustParse := func(tag string) Key {
		k, ok := Parse(tag)
		if !ok {
			t.Fatalf("Parse(%q) failed", tag)
		}
		return k
	}

	// Ascending chains; every element must sort below the next.
	chains := [][]string{
		{"1.9.0", "1.10.0", "2.0.0"},
		{"1.2", "1.2.1"},
		{"2.11.1", "2.11.1.1", "2.11.1.2", "2.11.2"},
		// Semver precedence among prereleases, and release above them all.
		{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta", "1.0.0-rc.1", "1.0.0"},
		{"2.0.0-rc.1", "2.0.0-rc.2", "2.0.0"},
		// A prerelease of a higher version still outranks a lower release by
		// number; it is filtering, not ordering, that keeps it out of a report.
		{"2.9.0", "3.0.0-rc1", "3.0.0"},
	}
	for _, chain := range chains {
		for i := 0; i+1 < len(chain); i++ {
			lo, hi := mustParse(chain[i]), mustParse(chain[i+1])
			if lo.Compare(hi) >= 0 {
				t.Errorf("%s should sort below %s", chain[i], chain[i+1])
			}
			if hi.Compare(lo) <= 0 {
				t.Errorf("%s should sort above %s", chain[i+1], chain[i])
			}
		}
	}

	// Equalities: build qualifiers and metadata take no part in comparison.
	equal := [][2]string{
		{"2.25.0-rel", "2.25.0-rel-build42"},
		{"1.2.3", "1.2.3+build5"},
		{"1.2", "1.2.0"},
		{"1.2.3", "v1.2.3"},
		{"2.11.1", "2.11.1.0"},
	}
	for _, pair := range equal {
		if c := mustParse(pair[0]).Compare(mustParse(pair[1])); c != 0 {
			t.Errorf("%s vs %s: Compare = %d, want 0", pair[0], pair[1], c)
		}
	}
}

func TestFriendly(t *testing.T) {
	tests := []struct{ tag, want string }{
		{"2.18.0-rel-build42", "2.18.0"},
		{"v1.2", "1.2.0"},
		{"01.02.03", "1.2.3"},
		// A release candidate must not be rendered as the release.
		{"3.0.0-rc1", "3.0.0-rc1"},
		{"custom-tag-meta-1-6-0", "custom-tag-meta-1-6-0"},
		{"latest", "latest"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Friendly(tt.tag); got != tt.want {
			t.Errorf("Friendly(%q) = %q; want %q", tt.tag, got, tt.want)
		}
	}
}

func TestPickTag(t *testing.T) {
	tests := []struct {
		name        string
		tags        []string
		prereleases bool
		want        string
	}{
		{
			// The reason this tool exists: push order is not version order.
			name: "highest version wins over push order",
			tags: []string{"1.9.0", "2.25.0-rel", "2.10.0"},
			want: "2.25.0-rel",
		},
		{
			name: "tie on version breaks to the shortest tag",
			tags: []string{"2.25.0-rel-build42", "2.25.0-rel"},
			want: "2.25.0-rel",
		},
		{
			name: "tie on version and length keeps the first",
			tags: []string{"2.0.0-aa", "2.0.0-bb"},
			want: "2.0.0-aa",
		},
		{
			name: "v-prefixed compares by number, not string",
			tags: []string{"v1.2.3", "1.2.2"},
			want: "v1.2.3",
		},
		{
			name: "missing patch is zero, so 1.2 beats 1.1.9",
			tags: []string{"1.1.9", "1.2"},
			want: "1.2",
		},
		{
			name: "1.2 and 1.2.0 are the same release, shortest wins",
			tags: []string{"1.2.0", "1.2"},
			want: "1.2",
		},
		{
			name: "leading zeros do not make a longer tag win",
			tags: []string{"1.02.0", "1.2.0"},
			want: "1.2.0",
		},
		{
			name: "extra component does not inflate the version",
			tags: []string{"1.2.3.4", "1.2.4"},
			want: "1.2.4",
		},
		{
			// A release candidate is not the latest version.
			name: "prereleases are excluded by default",
			tags: []string{"2.9.0", "3.0.0-rc1"},
			want: "2.9.0",
		},
		{
			name:        "prereleases included on request",
			tags:        []string{"2.9.0", "3.0.0-rc1"},
			prereleases: true,
			want:        "3.0.0-rc1",
		},
		{
			// Reporting "no version" would be worse than reporting the rc.
			name: "falls back to a prerelease when nothing is released",
			tags: []string{"3.0.0-rc1", "3.0.0-rc2"},
			want: "3.0.0-rc2",
		},
		{
			name: "no version-shaped tag falls back to first non-alias",
			tags: []string{"latest", "custom-build", "dev"},
			want: "custom-build",
		},
		{
			name: "only aliases falls back to the first tag",
			tags: []string{"latest", "stable"},
			want: "latest",
		},
		{
			name: "version beats an alias even when the alias is first",
			tags: []string{"latest", "3.1.4"},
			want: "3.1.4",
		},
		{
			name: "no tags at all",
			tags: nil,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PickTag(tt.tags, tt.prereleases); got != tt.want {
				t.Errorf("PickTag(%q, %v) = %q; want %q", tt.tags, tt.prereleases, got, tt.want)
			}
		})
	}
}

func TestIsFloating(t *testing.T) {
	if !isFloating("latest") {
		t.Error("latest should be floating")
	}
	if isFloating("2.1.0") {
		t.Error("2.1.0 should not be floating")
	}
}
