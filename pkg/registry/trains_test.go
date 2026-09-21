package registry

import "testing"

// A rolling "tip" pin carries its channel before the arch suffix; it must land
// in that channel's train even when only the per-arch tag has been pushed yet
// (sylve, 2026-09-13: it showed up under "latest").
func TestDiscoverTrainsArchSuffixedPinKeepsItsChannel(t *testing.T) {
	tags := []string{
		"latest", "nightly", "0.3.0", "0.3.1", "0.3.1-nightly",
		"0.3.1-tip.20260912.dcef065-nightly-amd64", // plain tag not published yet
	}
	tr := discoverTrains(tags, nil)
	for _, v := range tr["latest"] {
		if v.Tag == "0.3.1-tip.20260912.dcef065-nightly-amd64" {
			t.Fatalf("tip pin filed under latest: %+v", tr["latest"])
		}
	}
	found := false
	for _, v := range tr["nightly"] {
		if v.Tag == "0.3.1-tip.20260912.dcef065-nightly-amd64" && v.Version == "0.3.1-tip.20260912.dcef065" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tip pin missing from nightly: %+v", tr["nightly"])
	}
	if len(tr["latest"]) != 2 || tr["latest"][0].Version != "0.3.1" {
		t.Fatalf("latest train = %+v, want 0.3.1, 0.3.0", tr["latest"])
	}
}

// With the multi-arch tag present, the host-arch duplicate is dropped first.
func TestFilterArchTagsDropsRedundantHostArch(t *testing.T) {
	got := filterArchTags([]string{"0.3.1-nightly", "0.3.1-nightly-amd64", "0.3.1-nightly-aarch64", "latest", "latest-amd64"})
	for _, g := range got {
		if g == "0.3.1-nightly-amd64" || g == "0.3.1-nightly-aarch64" || g == "latest-amd64" {
			t.Fatalf("kept redundant/other-arch tag %q in %v", g, got)
		}
	}
}

// One rolling channel per major (redis): "8.6-pkg-latest" is a channel whose
// pins are "8.6.x-8.6-pkg-latest"; a stack on 8.6-pkg-latest must not be told
// that 8.10 (the 8.8 channel's current package) is an upgrade.
func TestDiscoverTrainsPerMajorChannels(t *testing.T) {
	tags := []string{
		"latest", "pkg", "pkg-latest",
		"8.6", "8.6-pkg-latest", "8.6.4-8.6-pkg-latest", "8.6.5-8.6-pkg-latest", "8.6.6-8.6-pkg-latest",
		"8.8", "8.8-pkg-latest", "8.8.0-8.8-pkg-latest", "8.8.1-8.8-pkg-latest", "8.10.0-8.8-pkg-latest", "8.10.1-8.8-pkg-latest",
		"8.6.3-pkg-latest", "8.8.0-pkg-latest", // older scheme, no major in the suffix
	}
	tr := discoverTrains(tags, nil)
	want := map[string][]string{
		"8.6-pkg-latest": {"8.6.6", "8.6.5", "8.6.4"},
		"8.8-pkg-latest": {"8.10.1", "8.10.0", "8.8.1", "8.8.0"},
		"pkg-latest":     {"8.8.0", "8.6.3"},
	}
	for ch, versions := range want {
		got := tr[ch]
		if len(got) != len(versions) {
			t.Fatalf("%s: got %+v, want %v", ch, got, versions)
		}
		for i, v := range versions {
			if got[i].Version != v {
				t.Fatalf("%s[%d] = %q, want %q (%+v)", ch, i, got[i].Version, v, got)
			}
		}
	}
	for ch, versions := range tr {
		for _, v := range versions {
			if v.Tag == "8.6-pkg-latest" || v.Tag == "8.8-pkg-latest" {
				t.Fatalf("channel tag %q listed as a version of %s", v.Tag, ch)
			}
		}
	}
}

// A per-major channel that has no "<version>-<channel>" pins of its own must
// still be recognised as a channel. postgres publishes rolling "18", "18-pkg"
// and "18-pkg-latest", but pins only "18.4-18" and "18.4-18-pkg-latest" --
// nothing is tagged "18.4-18-pkg". "18-pkg" was therefore filed as a VERSION
// of the "pkg" train, pooling every major together, and a stack on 17-pkg was
// offered an "upgrade" to 18-pkg: a major PostgreSQL jump that will not start
// on an existing data directory.
// Without a declared scheme, "14-pkg" and friends have to be guessed into
// channels by the prefix rule -- the fallback for a third-party image.
func TestDiscoverTrainsChannelWithoutOwnPins(t *testing.T) {
	tags := []string{
		"14", "14-pkg", "14-pkg-latest", "14.20-14", "14.20-14-pkg-latest",
		"17", "17-pkg", "17-pkg-latest", "17.10-17", "17.10-17-pkg-latest",
		"18", "18-pkg", "18-pkg-latest", "18.4-18", "18.4-18-pkg-latest",
		"17.7", "latest", "pkg", "pkg-latest",
	}
	tr := discoverTrains(tags, nil)

	for _, ch := range []string{"14-pkg", "17-pkg", "18-pkg"} {
		for train, vs := range tr {
			for _, v := range vs {
				if v.Tag == ch {
					t.Errorf("channel %q filed as a version in train %q (newest %s)", ch, train, vs[0].Tag)
				}
			}
		}
	}
	// The channel's own pins still land in it.
	if got := tr["18"]; len(got) != 1 || got[0].Tag != "18.4-18" {
		t.Errorf("train 18 = %v, want [18.4-18]", tagList(got))
	}
	// A bare "<version>" pin belongs to the default train, not to whichever
	// channel happens to sort last.
	if got := tr["latest"]; len(got) != 1 || got[0].Tag != "17.7" {
		t.Errorf("train latest = %v, want [17.7]", tagList(got))
	}
}

// postgres as its catalog entry declares it: "18-pkg" is an ALIAS of the "18"
// channel, so it is not a version of "pkg", and a stack installed from
// :18-pkg gets 18's versions rather than an empty train (and so never an
// update). Legacy "pkg"/"latest" pins from the retired scheme stay where they
// are: folding 18's versions onto "pkg" would offer 17.7-pkg a major jump.
func TestDiscoverTrainsDeclaredScheme(t *testing.T) {
	tags := []string{
		"14", "14-pkg", "14-pkg-latest", "14.20-14", "14.20-14-pkg-latest",
		"17", "17-pkg", "17-pkg-latest", "17.10-17", "17.10-17-pkg-latest",
		"18", "18-pkg", "18-pkg-latest", "18.4-18", "18.6-18", "18.4-18-pkg-latest",
		"17.7", "17.7-pkg", "latest", "pkg", "pkg-latest",
	}
	sch := &Scheme{
		Channels: []string{
			"14", "14-pkg", "14-pkg-latest",
			"17", "17-pkg", "17-pkg-latest",
			"18", "18-pkg", "pkg", "latest", "18-pkg-latest", "pkg-latest",
		},
		Aliases: map[string]string{
			"14-pkg": "14", "17-pkg": "17",
			"18-pkg": "18", "pkg": "18", "latest": "18", "pkg-latest": "18-pkg-latest",
		},
	}
	tr := discoverTrains(tags, sch)

	if got := tagList(tr["18"]); len(got) != 2 || got[0] != "18.6-18" {
		t.Errorf("train 18 = %v, want [18.6-18 18.4-18]", got)
	}
	// The alias follows the channel it names.
	if got := tagList(tr["18-pkg"]); len(got) != 2 || got[0] != "18.6-18" {
		t.Errorf("train 18-pkg = %v, want 18's versions", got)
	}
	// An alias with pins of its own keeps them.
	if got := tagList(tr["pkg"]); len(got) != 1 || got[0] != "17.7-pkg" {
		t.Errorf("train pkg = %v, want [17.7-pkg]", got)
	}
	if got := tagList(tr["latest"]); len(got) != 1 || got[0] != "17.7" {
		t.Errorf("train latest = %v, want [17.7]", got)
	}
}

// The prefix rule cannot tell a channel from a pre-release: "2.1-rc1" extends
// the "2.1" channel exactly the way "18-pkg" extends "18", so both are
// promoted. Declaring a scheme does not switch that off (see
// TestDiscoverTrainsSchemePredatingAliases), so the pre-release still becomes
// a train -- but an empty one, which offers nobody an upgrade, and it stays
// out of the real trains.
func TestDiscoverTrainsPreReleaseIsInert(t *testing.T) {
	tags := []string{"latest", "2.1", "2.1-rc1", "2.1.5-2.1", "2.1.6-2.1"}
	tr := discoverTrains(tags, &Scheme{Channels: []string{"latest", "2.1"}})

	if got := tagList(tr["2.1-rc1"]); len(got) != 0 {
		t.Errorf("train 2.1-rc1 = %v, want empty (nothing pins against it)", got)
	}
	if got := tagList(tr["2.1"]); len(got) != 2 || got[0] != "2.1.6-2.1" {
		t.Errorf("train 2.1 = %v, want [2.1.6-2.1 2.1.5-2.1]", got)
	}
	// Crucially it is not loose in the default train, where it would look
	// like the newest release to a stack on "latest".
	if got := tagList(tr["latest"]); len(got) != 0 {
		t.Errorf("train latest = %v, want empty", got)
	}
}

// A catalog fetched before aliases were published names "18" but not
// "18-pkg". The declared channels must not switch off the inference that
// covers the gap: filing "18-pkg" as a version of "pkg" would offer a stack
// on 14-pkg an upgrade to 18-pkg -- a major PostgreSQL jump onto an existing
// data directory, which is the whole thing this is meant to prevent.
func TestDiscoverTrainsSchemePredatingAliases(t *testing.T) {
	tags := []string{
		"14", "14-pkg", "14-pkg-latest", "14.20-14", "14.20-14-pkg-latest",
		"18", "18-pkg", "18-pkg-latest", "18.6-18", "18.6-18-pkg-latest",
		"17.7-pkg", "latest", "pkg", "pkg-latest",
	}
	// Ids only -- no aliases, the shape published today.
	sch := &Scheme{Channels: []string{"14", "14-pkg-latest", "18", "18-pkg-latest"}}
	tr := discoverTrains(tags, sch)

	if got := tagList(tr["pkg"]); len(got) != 1 || got[0] != "17.7-pkg" {
		t.Fatalf("train pkg = %v, want [17.7-pkg] -- a channel tag leaked in as a version", got)
	}
	for _, ch := range []string{"14-pkg", "18-pkg"} {
		if _, ok := tr[ch]; !ok {
			t.Errorf("%q did not survive as a train", ch)
		}
	}
}

// A declared channel the registry no longer publishes must not appear as an
// empty train in the picker.
func TestDiscoverTrainsDeclaredSchemeIgnoresUnpublished(t *testing.T) {
	tr := discoverTrains([]string{"latest", "1.2"}, &Scheme{Channels: []string{"latest", "edge"}})
	if _, ok := tr["edge"]; ok {
		t.Errorf("unpublished channel edge became a train: %v", tr)
	}
}

func tagList(vs []Version) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Tag)
	}
	return out
}
