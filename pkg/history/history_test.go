package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	l := New(t.TempDir())
	t0 := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	for i, a := range []string{Install, Update, Rollback} {
		if err := l.Append(Entry{At: t0.Add(time.Duration(i) * time.Minute), Stack: "radarr", Action: a, Trigger: ByYou, Result: OK}); err != nil {
			t.Fatal(err)
		}
	}
	l.Append(Entry{At: t0.Add(90 * time.Second), Stack: "sonarr", Action: Start, Trigger: ByBoot, Result: Failed, Message: "port taken"})

	got := l.Stack("radarr", 0)
	if len(got) != 3 || got[0].Action != Rollback || got[2].Action != Install {
		t.Fatalf("radarr newest first: %+v", got)
	}
	if got := l.Stack("radarr", 1); len(got) != 1 || got[0].Action != Rollback {
		t.Fatalf("limit: %+v", got)
	}
	all := l.All(0)
	if len(all) != 4 || all[0].Action != Rollback || all[1].Stack != "sonarr" {
		t.Fatalf("all, newest first across stacks: %+v", all)
	}
}

// A deleted stack has no directory left, but its history stays readable.
func TestKeptAfterDelete(t *testing.T) {
	root := t.TempDir()
	l := New(root)
	l.Append(Entry{Stack: "immich", Action: Delete, Trigger: ByYou, Result: OK})
	if got := New(root).All(0); len(got) != 1 || got[0].Action != Delete || got[0].At.IsZero() {
		t.Fatalf("%+v", got)
	}
}

func TestTrim(t *testing.T) {
	l := New(t.TempDir())
	for i := 0; i < Keep+Keep/4+1; i++ {
		l.Append(Entry{At: time.Unix(int64(i), 0), Stack: "a", Action: Start, Result: OK})
	}
	got := l.Stack("a", 0)
	if len(got) != Keep || got[0].At.Unix() != int64(Keep+Keep/4) || got[Keep-1].At.Unix() != int64(Keep/4+1) {
		t.Fatalf("kept %d, newest %d, oldest %d", len(got), got[0].At.Unix(), got[len(got)-1].At.Unix())
	}
}

// A line cut short by a crash is skipped; the rest still reads.
func TestTornLine(t *testing.T) {
	root := t.TempDir()
	l := New(root)
	l.Append(Entry{Stack: "a", Action: Start, Result: OK})
	f, _ := os.OpenFile(filepath.Join(root, "history", "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"at":"2026-11-23T03:00:00Z","stack":"a","act`)
	f.Close()
	if got := l.Stack("a", 0); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	l.Append(Entry{Stack: "a", Action: Stop, Result: OK})
	if got := l.Stack("a", 0); len(got) != 2 {
		t.Fatalf("an append after a torn line: %+v", got)
	}
}

func TestBadName(t *testing.T) {
	l := New(t.TempDir())
	if err := l.Append(Entry{Stack: "../etc/passwd", Action: Start}); err == nil {
		t.Fatal("a path as a stack name was written")
	}
	if l.Stack("../x", 0) != nil {
		t.Fatal("read a path")
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("  pull failed\nmore\n"); got != "pull failed" {
		t.Fatalf("%q", got)
	}
}
