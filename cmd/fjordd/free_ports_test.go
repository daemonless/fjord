package main

import "testing"

func TestListenerNames(t *testing.T) {
	bsd := "USER     COMMAND    PID   FD  PROTO  LOCAL ADDRESS         FOREIGN ADDRESS\nroot     dnsmasq    812   6   tcp4   *:53                  *:*\n"
	if got := sockstatCommand(bsd); got != "dnsmasq" {
		t.Errorf("sockstat: %q", got)
	}
	linux := `LISTEN 0 4096 0.0.0.0:3000 0.0.0.0:* users:(("conmon",pid=1940971,fd=5))`
	if got := ssCommand(linux); got != "conmon" {
		t.Errorf("ss: %q", got)
	}
	if sockstatCommand("USER COMMAND PID\n") != "" || ssCommand("") != "" {
		t.Error("nothing listening: want no name")
	}
}
