package appjail

import (
	"strings"
	"testing"
)

func TestProgressWriterNumbersServices(t *testing.T) {
	var out strings.Builder
	p := newProgressWriter(&out, 2)
	// As director writes it: the "Creating" start flushed, "Done." later,
	// and the pieces split anywhere.
	for _, s := range []string{
		"$ appjail-director up\n",
		"Starting Director (project:crafting_apps) ...\n",
		"Creat", "ing web (crafting_apps_web) ... ",
		"Done.\n",
		"Starting web (crafting_apps_web) ... Done.\n",
		"Creating db (d) ... ", "FAIL!\n",
		"Creating cache (c) ... Done.\nFinished: crafting_apps\n",
	} {
		if _, err := p.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	want := "$ appjail-director up\n" +
		"Starting Director (project:crafting_apps) ...\n" +
		"[1/2] Creating web (crafting_apps_web) ... Done.\n" +
		"[1/2] Starting web (crafting_apps_web) ... Done.\n" +
		"[2/2] Creating db (d) ... FAIL!\n" +
		"[3/3] Creating cache (c) ... Done.\n" +
		"Finished: crafting_apps\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// The wait for a jail must show while it builds, not when "Done." arrives.
func TestProgressWriterPassesTheWaitThrough(t *testing.T) {
	var out strings.Builder
	p := newProgressWriter(&out, 1)
	p.Write([]byte("Creating web (w) ... "))
	if got := out.String(); got != "[1/1] Creating web (w) ... " {
		t.Fatalf("held back: %q", got)
	}
}

func TestUpSummary(t *testing.T) {
	for _, c := range []struct {
		build, start int
		want         string
	}{
		{1, 0, "one at a time: 1 service to build. This takes a few moments."},
		{7, 0, "7 services to build."},
		{2, 1, "2 services to build, 1 to start."},
	} {
		if got := upSummary(c.build, c.start); !strings.Contains(got, c.want) {
			t.Errorf("upSummary(%d, %d) = %q, want %q in it", c.build, c.start, got, c.want)
		}
	}
}
