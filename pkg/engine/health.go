package engine

import "strings"

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
