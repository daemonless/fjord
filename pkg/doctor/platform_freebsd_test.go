package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real numbers from jupiter, 2026-09-22. podman_service had been up since
// Sep 6; `pkg upgrade` replaced podman under it on Sep 21. Every stack's shell
// returned 500 "no such file or directory" from the API while `podman exec` on
// the command line worked, so it read as a fjord bug rather than a host one.
func TestServiceStaleCatchesTheJupiterIncident(t *testing.T) {
	started := time.Date(2026, 9, 6, 0, 23, 12, 0, time.Local)
	installed := time.Date(2026, 9, 21, 18, 28, 0, 0, time.Local)

	st, msg := serviceStale(started, installed, "podman")
	if st != Warn {
		t.Fatalf("status = %v, want Warn: a service older than its package is the whole bug", st)
	}
	for _, want := range []string{"Sep 6", "podman", "Sep 21", "restarted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should mention %q, got %q", want, msg)
		}
	}
}

// Restarted after the upgrade, which is the state every healthy host is in.
func TestServiceStaleQuietWhenCurrent(t *testing.T) {
	installed := time.Date(2026, 9, 21, 18, 28, 0, 0, time.Local)
	started := installed.Add(2 * time.Hour)
	if st, _ := serviceStale(started, installed, "podman"); st != OK {
		t.Errorf("status = %v, want OK", st)
	}
	// Nothing to compare against is not a problem either.
	if st, _ := serviceStale(started, time.Time{}, ""); st != OK {
		t.Errorf("status = %v with no package time, want OK", st)
	}
}

// versionBelow compares every part it is given, not just major.minor.
func TestVersionBelowComparesEveryPartAskedFor(t *testing.T) {
	for _, c := range []struct {
		v    string
		want []int
		old  bool
	}{
		{"0.6.0", []int{0, 6, 1}, true},
		{"0.6.1", []int{0, 6, 1}, false},
		{"0.6.2", []int{0, 6, 1}, false},
		{"0.7", []int{0, 6, 1}, false},
		{"0.6", []int{0, 6, 1}, true}, // a missing part counts as 0
		{"0.5.9", []int{0, 6, 1}, true},
		{"5.4.2", []int{5, 5}, true},
		{"5.5.0", []int{5, 5}, false},
		{"5.7.0", []int{5, 5}, false},
		{"", []int{5, 5}, false}, // unknown: no warning
	} {
		if got := versionBelow(c.v, c.want...); got != c.old {
			t.Errorf("versionBelow(%q, %v) = %v, want %v", c.v, c.want, got, c.old)
		}
	}
}

// A host with no /etc/localtime (FreeBSD's VM images) cannot start an AppJail
// jail; the check must say so, and name the zone once there is one.
func TestLocaltimeProbe(t *testing.T) {
	dir := t.TempDir()
	lt := filepath.Join(dir, "localtime")
	if st, _ := localtimeProbe(lt)(context.Background()); st != Fail {
		t.Fatalf("missing localtime: %s, want fail", st)
	}
	zone := filepath.Join(dir, "UTC")
	os.WriteFile(zone, []byte("TZif"), 0o644)
	os.Symlink(zone, lt)
	if st, d := localtimeProbe(lt)(context.Background()); st != OK || d != zone {
		t.Fatalf("symlinked zone: %s %q", st, d)
	}
	os.Remove(zone) // dangling link: still no timezone
	if st, _ := localtimeProbe(lt)(context.Background()); st != Fail {
		t.Fatalf("dangling localtime: %s, want fail", st)
	}
}

// appjail-dns is only "running" when dnsmasq answers with appjail's config;
// each missing piece is named, in the order it gets fixed.
func TestAppjailDNSStatus(t *testing.T) {
	conf := appjailDNSMasqConf
	cases := []struct {
		name                                        string
		installed, dnsmasqInstalled, dnsmasqRunning bool
		dnsmasqConf, dnsmasqArgs                    string
		appjailRunning                              bool
		want                                        Status
		saying                                      string
	}{
		{"nothing installed", false, false, false, "", "", false, Warn, "appjail-dns is not installed"},
		{"no dnsmasq", true, false, false, "", "", true, Warn, "dnsmasq is not installed"},
		{"dnsmasq stopped (fjordfresh, netlab)", true, true, false, "", "", true, Warn, "dnsmasq is not running"},
		{"dnsmasq on its own config", true, true, true, "/usr/local/etc/dnsmasq.conf", "123 dnsmasq -C /usr/local/etc/dnsmasq.conf", true, Warn, "not with appjail's config"},
		{"dnsmasq right, appjail-dns stopped", true, true, true, conf, "", false, Warn, "appjail-dns is not running"},
		{"all there, by rc var", true, true, true, conf, "", true, OK, "running"},
		{"all there, config on the command line", true, true, true, "", "123 dnsmasq -C " + conf, true, OK, "running"},
	}
	for _, c := range cases {
		got, msg := appjailDNSStatus(c.installed, c.dnsmasqInstalled, c.dnsmasqRunning, c.dnsmasqConf, c.dnsmasqArgs, c.appjailRunning)
		if got != c.want || !strings.Contains(msg, c.saying) {
			t.Errorf("%s: %v %q, want %v saying %q", c.name, got, msg, c.want, c.saying)
		}
	}
}
