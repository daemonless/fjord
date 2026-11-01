package doctor

import "testing"

func TestFirewallVerdict(t *testing.T) {
	if s, _ := firewallVerdict("", false, "3567"); s != OK {
		t.Error("no firewall should be ok")
	}
	if s, _ := firewallVerdict("firewalld", true, "3567"); s != OK {
		t.Error("allowed should be ok")
	}
	if s, d := firewallVerdict("firewalld", false, "3567"); s != Warn || d == "" {
		t.Errorf("blocked: %s %q", s, d)
	}
}

// `ufw status` as Ubuntu prints it.
func TestUfwAllows(t *testing.T) {
	out := "Status: active\n\nTo                         Action      From\n--                         ------      ----\n22/tcp                     ALLOW       Anywhere\n3567/tcp                   ALLOW       Anywhere\n"
	if !ufwAllows(out, "3567") {
		t.Error("3567/tcp ALLOW not seen")
	}
	if ufwAllows(out, "8080") {
		t.Error("8080 is not allowed")
	}
	if ufwAllows("Status: active\n3567/tcp DENY Anywhere\n", "3567") {
		t.Error("DENY read as allowed")
	}
}
