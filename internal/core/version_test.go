package core

import (
	"strings"
	"testing"
)

func TestParseSemVerValid(t *testing.T) {
	cases := []struct {
		in    string
		major uint64
		minor uint64
		patch uint64
		pre   string
		build string
	}{
		{"0.0.0", 0, 0, 0, "", ""},
		{"1.0.0", 1, 0, 0, "", ""},
		{"1.0.0-beta.6", 1, 0, 0, "beta.6", ""},
		{"1.2.3-alpha.1+build.5", 1, 2, 3, "alpha.1", "build.5"},
		{"1.0.0+dev.abc123", 1, 0, 0, "", "dev.abc123"},
		{"1.0.0-beta.6+dev", 1, 0, 0, "beta.6", "dev"},
		{"1.0.0-0.3.7", 1, 0, 0, "0.3.7", ""},
		{"1.0.0-x-y-z.--", 1, 0, 0, "x-y-z.--", ""},
		{"1.0.0+001", 1, 0, 0, "", "001"},
		{"10.20.30", 10, 20, 30, "", ""},
	}
	for _, c := range cases {
		v, err := ParseSemVer(c.in)
		if err != nil {
			t.Errorf("ParseSemVer(%q) error: %v", c.in, err)
			continue
		}
		if v.Major != c.major || v.Minor != c.minor || v.Patch != c.patch {
			t.Errorf("ParseSemVer(%q) core = %d.%d.%d", c.in, v.Major, v.Minor, v.Patch)
		}
		if got := strings.Join(v.Pre, "."); got != c.pre {
			t.Errorf("ParseSemVer(%q) pre = %q, want %q", c.in, got, c.pre)
		}
		if got := strings.Join(v.Build, "."); got != c.build {
			t.Errorf("ParseSemVer(%q) build = %q, want %q", c.in, got, c.build)
		}
		if v.String() != c.in {
			t.Errorf("String() = %q, want %q", v.String(), c.in)
		}
	}
}

func TestParseSemVerInvalid(t *testing.T) {
	cases := []string{
		"",
		" ",
		"v1.0.0",
		"V1.0.0",
		"1",
		"1.0",
		"1.0.0.0",
		"1.0.",
		".1.0",
		"1..0",
		"01.0.0",
		"1.00.0",
		"1.0.01",
		"-1.0.0",
		"a.b.c",
		"1.0.0-",
		"1.0.0+",
		"1.0.0-beta.",
		"1.0.0-.beta",
		"1.0.0-beta..1",
		"1.0.0-01",
		"1.0.0-beta.06",
		"1.0.0-be_ta",
		"1.0.0+build..1",
		"1.0.0+bu!ld",
		"1.0.0 ",
		" 1.0.0",
		"18446744073709551616.0.0",
	}
	for _, in := range cases {
		if v, err := ParseSemVer(in); err == nil {
			t.Errorf("ParseSemVer(%q) = %v, want error", in, v)
		} else if !strings.HasPrefix(err.Error(), "parse version ") {
			t.Errorf("ParseSemVer(%q) error %q should start with the failure", in, err)
		}
	}
}

func TestCompareChain(t *testing.T) {
	chains := [][]string{
		// the example chain in the SemVer 2.0.0 precedence section
		{
			"1.0.0-alpha",
			"1.0.0-alpha.1",
			"1.0.0-alpha.beta",
			"1.0.0-beta",
			"1.0.0-beta.2",
			"1.0.0-beta.11",
			"1.0.0-rc.1",
			"1.0.0",
		},
		// core fields compare numerically, not as text
		{"0.9.9", "1.0.0", "1.0.1", "1.1.0", "1.9.0", "1.10.0", "2.0.0", "10.0.0"},
		// the release line
		{"1.0.0-beta.5", "1.0.0-beta.6", "1.0.0-beta.10", "1.0.0"},
		// numeric identifiers rank below alphanumeric ones
		{"1.0.0-1", "1.0.0-2", "1.0.0-10", "1.0.0-a"},
		// uppercase sorts before lowercase in ASCII order
		{"1.0.0-Z", "1.0.0-a"},
		// numeric identifiers past uint64 still compare by magnitude
		{"1.0.0-99999999999999999999", "1.0.0-100000000000000000000"},
	}
	for _, chain := range chains {
		for i := range chain {
			for j := range chain {
				a, b := mustParse(t, chain[i]), mustParse(t, chain[j])
				want := 0
				if i < j {
					want = -1
				} else if i > j {
					want = 1
				}
				if got := a.Compare(b); got != want {
					t.Errorf("Compare(%s, %s) = %d, want %d", chain[i], chain[j], got, want)
				}
			}
		}
	}
}

func TestCompareIgnoresBuildMetadata(t *testing.T) {
	pairs := [][2]string{
		{"1.0.0+a", "1.0.0+b"},
		{"1.0.0", "1.0.0+dev.abc123"},
		{"1.0.0-beta.6+dev", "1.0.0-beta.6"},
		{"1.0.0-beta.6+001", "1.0.0-beta.6+20130313144700"},
	}
	for _, p := range pairs {
		a, b := mustParse(t, p[0]), mustParse(t, p[1])
		if a.Compare(b) != 0 || b.Compare(a) != 0 {
			t.Errorf("Compare(%s, %s) != 0", p[0], p[1])
		}
	}
}

func TestMeetsPin(t *testing.T) {
	cases := []struct {
		have string
		min  string
		want bool
	}{
		{"1.0.0-beta.6", "1.0.0-beta.6", true},
		{"1.0.0-beta.6+dev", "1.0.0-beta.6", true},
		{"1.0.0-beta.10", "1.0.0-beta.6", true},
		{"1.0.0", "1.0.0-beta.6", true},
		{"1.0.0-beta.5", "1.0.0-beta.6", false},
		{"1.0.0-beta.6", "1.0.0-beta.10", false},
		{"1.0.0-rc.1", "1.0.0", false},
		{"0.9.9", "1.0.0-beta.6", false},
	}
	for _, c := range cases {
		got, err := MeetsPin(c.have, c.min)
		if err != nil {
			t.Errorf("MeetsPin(%q, %q) error: %v", c.have, c.min, err)
			continue
		}
		if got != c.want {
			t.Errorf("MeetsPin(%q, %q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestMeetsPinInvalid(t *testing.T) {
	if _, err := MeetsPin("bogus", "1.0.0"); err == nil {
		t.Error("MeetsPin with a bad binary version should fail")
	}
	if _, err := MeetsPin("1.0.0", "v1.0.0"); err == nil {
		t.Error("MeetsPin with a bad pin should fail")
	}
}

func TestBinaryVersion(t *testing.T) {
	if _, err := ParseSemVer(BinaryVersion()); err != nil {
		t.Fatalf("embedded version %q is not SemVer: %v", BinaryVersion(), err)
	}
	// An unstamped dev build must satisfy the pin this repo carries.
	ok, err := BinaryMeetsPin("1.0.0-beta.6")
	if err != nil || !ok {
		t.Errorf("BinaryMeetsPin(1.0.0-beta.6) = %v, %v; want true, nil", ok, err)
	}
	if ok, _ := BinaryMeetsPin("1.0.0-beta.7"); ok {
		t.Error("dev build must not meet a pin above it")
	}
}

// mustParse parses s or fails the test.
func mustParse(t *testing.T, s string) SemVer {
	t.Helper()
	v, err := ParseSemVer(s)
	if err != nil {
		t.Fatalf("ParseSemVer(%q): %v", s, err)
	}
	return v
}
