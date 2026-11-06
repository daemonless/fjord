package doctor

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestFjorddOutdatedStatus(t *testing.T) {
	started := time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local)
	if st, _ := fjorddOutdatedStatus(started, started.Add(-time.Hour)); st != OK {
		t.Errorf("binary older than process: %v, want OK", st)
	}
	if st, _ := fjorddOutdatedStatus(started, started.Add(time.Second)); st != OK {
		t.Errorf("binary within 2s grace of start: %v, want OK", st)
	}
	st, msg := fjorddOutdatedStatus(started, started.Add(30*time.Minute))
	if st != Warn || !strings.Contains(msg, "restart fjordd") {
		t.Errorf("binary newer than process: %v %q, want Warn mentioning restart", st, msg)
	}
}

func TestStorageLowStatus(t *testing.T) {
	if st, _ := storageLowStatus("/var/db/fjord", 20<<30); st != OK {
		t.Errorf("20 GB free: %v, want OK", st)
	}
	if st, msg := storageLowStatus("/var/db/fjord", 3<<30); st != Warn || !strings.Contains(msg, "running low") {
		t.Errorf("3 GB free: %v %q, want Warn", st, msg)
	}
	if st, msg := storageLowStatus("/var/db/fjord", 512<<20); st != Fail || !strings.Contains(msg, "full disk") {
		t.Errorf("512 MB free: %v %q, want Fail", st, msg)
	}
}

func TestParseGraphRoot(t *testing.T) {
	conf := "[storage]\ndriver = \"zfs\"\n# comment\ngraphroot = \"/var/db/containers/storage\"\n"
	if got := parseGraphRoot(conf); got != "/var/db/containers/storage" {
		t.Errorf("parseGraphRoot = %q, want /var/db/containers/storage", got)
	}
}

func TestHostnameTakenStatus(t *testing.T) {
	own := []net.IP{net.ParseIP("192.168.4.10"), net.ParseIP("127.0.0.1")}
	if st, _ := hostnameTakenStatus("host.lan", []net.IP{net.ParseIP("192.168.4.10")}, own); st != OK {
		t.Errorf("matching address: %v, want OK", st)
	}
	if st, _ := hostnameTakenStatus("host.lan", []net.IP{net.ParseIP("127.0.0.1")}, own); st != OK {
		t.Errorf("loopback only: %v, want OK", st)
	}
	// Public IP behind NAT must not misfire.
	if st, _ := hostnameTakenStatus("vps.example.com", []net.IP{net.ParseIP("203.0.113.10")}, own); st != OK {
		t.Errorf("public IP behind NAT: %v, want OK", st)
	}
	st, msg := hostnameTakenStatus("host.lan", []net.IP{net.ParseIP("192.168.4.205")}, own)
	if st != Warn || !strings.Contains(msg, "192.168.4.205") {
		t.Errorf("taken address: %v %q, want Warn naming 192.168.4.205", st, msg)
	}
}
