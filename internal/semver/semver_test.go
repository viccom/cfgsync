package semver

import "testing"

func TestParse_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want Parsed
	}{
		{"1.0.0", Parsed{1, 0, 0, ""}},
		{"v2.3.4", Parsed{2, 3, 4, ""}},
		{"0.0.1", Parsed{0, 0, 1, ""}},
		{"1.0.0-rc.1", Parsed{1, 0, 0, "rc.1"}},
		{"1.2.3-alpha.2.beta", Parsed{1, 2, 3, "alpha.2.beta"}},
		{"10.20.30", Parsed{10, 20, 30, ""}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.in, got, c.want)
			continue
		}
		// String() must always emit the canonical "v"-less form, even when
		// the input had a leading "v".
		if got.String() != c.want.String() {
			t.Errorf("String() = %q, want %q", got.String(), c.want.String())
		}
	}
}

func TestParse_Invalid(t *testing.T) {
	cases := []string{
		"",
		"1",
		"1.0",
		"1.0.0.0",
		"01.0.0", // leading zeros rejected by semver
		"1.0.0+build", // build metadata rejected by cfgsync policy
		"1.0.0-",
		"v",
		"vX.Y.Z",
		" 1.0.0",
		"1.0.0 ",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q) expected error, got nil", c)
		}
	}
}

func TestCompare_Precedence(t *testing.T) {
	// Examples lifted from semver.org §11.
	ordered := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	parsed := make([]Parsed, len(ordered))
	for i, s := range ordered {
		p, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		parsed[i] = p
	}
	for i := 0; i < len(parsed)-1; i++ {
		if c := parsed[i].Compare(parsed[i+1]); c >= 0 {
			t.Errorf("%s.Compare(%s) = %d, want < 0", ordered[i], ordered[i+1], c)
		}
		// Antisymmetry.
		if c := parsed[i+1].Compare(parsed[i]); c <= 0 {
			t.Errorf("%s.Compare(%s) = %d, want > 0", ordered[i+1], ordered[i], c)
		}
	}
	// Reflexivity.
	if c := parsed[0].Compare(parsed[0]); c != 0 {
		t.Errorf("self-Compare = %d, want 0", c)
	}
}

func TestCompare_MajorMinor(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.0.0", "1.9.9", 1},
		{"1.2.0", "1.3.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
	}
	for _, c := range cases {
		a, _ := Parse(c.a)
		b, _ := Parse(c.b)
		got := sign(a.Compare(b))
		if got != c.want {
			t.Errorf("%s vs %s: got %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}
