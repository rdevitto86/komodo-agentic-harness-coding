// Package core holds plumbing with no domain logic.
package core

import (
	"fmt"
	"strconv"
	"strings"
)

// SemVer is a parsed Semantic Versioning 2.0.0 version.
type SemVer struct {
	Major, Minor, Patch uint64   // the numeric core
	Pre                 []string // prerelease identifiers, empty for a release
	Build               []string // build metadata identifiers, ignored by Compare
}

// version is the release this binary reports; a release build overrides it with -ldflags "-X".
// An unstamped build keeps the +dev marker, so it meets the current pin and only a newer release replaces it.
var version = "1.0.0-beta.6+dev"

// String renders the version in canonical SemVer form, build metadata included.
func (v SemVer) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if len(v.Build) > 0 {
		s += "+" + strings.Join(v.Build, ".")
	}
	return s
}

// Compare returns -1, 0 or 1 by SemVer precedence; build metadata never affects the result.
func (v SemVer) Compare(o SemVer) int {
	if c := cmpUint(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpUint(v.Patch, o.Patch); c != 0 {
		return c
	}
	// A prerelease ranks below the release it precedes.
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}

	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := cmpIdentifier(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	// With equal leading identifiers, the longer list ranks higher.
	return cmpUint(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

// BinaryVersion returns the version string embedded in this binary.
func BinaryVersion() string { return version }

// BinaryMeetsPin reports whether this binary's version is at or above pin.
func BinaryMeetsPin(pin string) (bool, error) { return MeetsPin(version, pin) }

// MeetsPin reports whether have is at or above pin by SemVer precedence; both must parse.
func MeetsPin(have, pin string) (bool, error) {
	h, err := ParseSemVer(have)
	if err != nil {
		return false, err
	}

	p, err := ParseSemVer(pin)
	if err != nil {
		return false, fmt.Errorf("pin: %w", err)
	}

	return h.Compare(p) >= 0, nil
}

// ParseSemVer parses a strict SemVer 2.0.0 string; a leading "v", spaces and leading zeros are errors.
func ParseSemVer(s string) (SemVer, error) {
	var v SemVer

	rest, build, hasBuild := strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(rest, "-")
	nums := strings.Split(core, ".")
	if len(nums) != 3 {
		return v, parseErr(s, "want major.minor.patch")
	}

	var fields [3]uint64

	for i, n := range nums {
		if !isDigits(n) {
			return v, parseErr(s, "core field "+strconv.Quote(n)+" is not a number")
		}
		if len(n) > 1 && n[0] == '0' {
			return v, parseErr(s, "core field "+strconv.Quote(n)+" has a leading zero")
		}

		u, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return v, parseErr(s, "core field "+strconv.Quote(n)+" is out of range")
		}

		fields[i] = u
	}

	v.Major, v.Minor, v.Patch = fields[0], fields[1], fields[2]
	if hasPre {
		ids, err := splitIdentifiers(pre, true)
		if err != nil {
			return SemVer{}, parseErr(s, "prerelease "+err.Error())
		}
		v.Pre = ids
	}
	if hasBuild {
		ids, err := splitIdentifiers(build, false)
		if err != nil {
			return SemVer{}, parseErr(s, "build metadata "+err.Error())
		}
		v.Build = ids
	}

	return v, nil
}

// cmpIdentifier orders two prerelease identifiers: numbers by value and below text, text by ASCII.
func cmpIdentifier(a, b string) int {
	an, bn := isDigits(a), isDigits(b)
	switch {
	case an && bn:
		// Digit strings have no leading zeros, so length then text is numeric order without overflow.
		if c := cmpUint(uint64(len(a)), uint64(len(b))); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}

	return strings.Compare(a, b)
}

// isDigits reports whether s is non-empty and made only of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

// cmpUint returns -1, 0 or 1 as a is less than, equal to or greater than b.
func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// splitIdentifiers splits a dot-separated identifier list, rejecting empty or non-[0-9A-Za-z-] ones. With noLeadingZero set,
// a numeric identifier with a leading zero is also rejected.
func splitIdentifiers(s string, noLeadingZero bool) ([]string, error) {
	ids := strings.Split(s, ".")
	for _, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("has an empty identifier")
		}

		for i := 0; i < len(id); i++ {
			c := id[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return nil, fmt.Errorf("identifier %q has an invalid character", id)
			}
		}
		if noLeadingZero && len(id) > 1 && id[0] == '0' && isDigits(id) {
			return nil, fmt.Errorf("identifier %q has a leading zero", id)
		}
	}

	return ids, nil
}

// parseErr builds the error for a version string that fails to parse.
func parseErr(s, reason string) error { return fmt.Errorf("parse version %q: %s", s, reason) }
