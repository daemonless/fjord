package updates

import (
	"regexp"
	"strconv"
	"strings"
)

// Class is how big an update is, which is what an update policy acts on.
type Class string

const (
	// Rebuild: the app version did not move -- only packages underneath it,
	// or a build suffix (a FreeBSD port revision, a linuxserver -lsNNN).
	Rebuild Class = "rebuild"
	Patch   Class = "patch"
	Minor   Class = "minor"
	Major   Class = "major"
	// Unknown: no version to compare, or one that has no order to it (a
	// date). Only the most permissive policy acts on it.
	Unknown Class = "unknown"
)

// numericVersion is the leading dotted-number part of a version, after an
// optional "v": "v2.18.1" -> 2.18.1, "2.9.0_2" -> 2.9.0, "1.30.4-r1-ls376"
// -> 1.30.4. What follows is a build suffix, not a version.
var numericVersion = regexp.MustCompile(`^[vV]?(\d+(?:\.\d+)*)`)

// dateVersion is a calendar version (organizr's "2026-05-19"). Its parts are
// not major/minor/patch, and reading them as such would call every month a
// minor update.
var dateVersion = regexp.MustCompile(`^\d{4}[-.]\d{2}[-.]\d{2}`)

// Classify says how far from -> to moves.
//
//   - equal, or only a build suffix moved          -> Rebuild
//   - first number moved                           -> Major
//   - second                                       -> Minor
//   - third or later (Servarr's 4th is a build no.) -> Patch
//   - either side missing, non-numeric, or a date  -> Unknown
func Classify(from, to string) Class {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return Unknown
	}
	if from == to {
		return Rebuild
	}
	if dateVersion.MatchString(from) || dateVersion.MatchString(to) {
		return Unknown
	}
	a, b := numericVersion.FindStringSubmatch(from), numericVersion.FindStringSubmatch(to)
	if a == nil || b == nil {
		return Unknown
	}
	pa, pb := strings.Split(a[1], "."), strings.Split(b[1], ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		if part(pa, i) == part(pb, i) {
			continue
		}
		switch i {
		case 0:
			return Major
		case 1:
			return Minor
		default:
			return Patch
		}
	}
	return Rebuild // numbers equal: only the suffix moved
}

// part is the i'th number of a version, 0 past its end ("1.4" == "1.4.0").
func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}
