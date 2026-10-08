package engine

import (
	"regexp"
	"strings"
)

// CrashedInLog reports whether a container log tail shows s6 restarting a
// service after a genuine fault.
//
// s6 logs a deliberate stop with the same wording it uses for a fault:
//
//	[s6] Service 'zensical' crashed (Exit: 256, Signal: 15)
//
// Signal 15 is SIGTERM -- that line IS the shutdown, so a stack that was
// stopped and started again reads as crashed for as long as it stays in the
// tail. Only faults we did not cause count.
func CrashedInLog(t string) bool {
	for _, ln := range strings.Split(t, "\n") {
		if !strings.Contains(ln, "] Service '") || !strings.Contains(ln, "crashed") {
			continue
		}
		if strings.Contains(ln, "Signal: 15") || strings.Contains(ln, "Signal: 2") {
			continue // SIGTERM / SIGINT: a stop, not a fault
		}
		return true
	}
	return false
}

// errLine is an app's own error report: slog's level=ERROR, Python's
// ModuleNotFoundError, a Go panic.
var errLine = regexp.MustCompile(`(?i)(error|fatal|panic|exception)`)

// slogMsg is the msg="..." of a structured log line.
var slogMsg = regexp.MustCompile(`msg="((?:[^"\\]|\\.)*)"`)

// CrashReason is the app's last error line in t (a log tail), "" when it
// wrote none, so a crash says why instead of "see Logs".
func CrashReason(t string) string {
	var reason string
	for _, ln := range strings.Split(t, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "[s6]") || strings.Contains(ln, "] Service '") {
			continue
		}
		if errLine.MatchString(ln) {
			reason = ln
		}
	}
	if m := slogMsg.FindStringSubmatch(reason); m != nil {
		reason = strings.ReplaceAll(m[1], `\"`, `"`)
	}
	if len(reason) > 200 {
		reason = reason[:199] + "…"
	}
	return reason
}
