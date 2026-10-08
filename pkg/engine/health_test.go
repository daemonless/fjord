package engine

import "testing"

// Verbatim from zensical's container log after a director down/up: s6 reports
// a deliberate SIGTERM stop with the same "crashed" wording it uses for a
// fault, and the app then starts and serves. Reading this as a crash reported
// a healthy stack as broken.
const restartedLog = `[s6] Service 'zensical' crashed (Exit: 256, Signal: 15)
[s6] Waiting 5 seconds before restart...
[init] Starting container initialization...
[init] Initialization complete
[init] Starting s6 supervision...
Serving /config/site on http://0.0.0.0:8000
Build started
No issues found`

// A genuine fault: the app died on its own, not because we stopped it.
const faultingLog = `[init] Starting s6 supervision...
[s6] Service 'app' crashed (Exit: 1, Signal: 0)
[s6] Waiting 5 seconds before restart...
[s6] Service 'app' crashed (Exit: 1, Signal: 0)`

func TestCrashLogIgnoresDeliberateStop(t *testing.T) {
	if CrashedInLog(restartedLog) {
		t.Error("a SIGTERM stop was read as a crash")
	}
	if !CrashedInLog(faultingLog) {
		t.Error("a real crash loop was missed")
	}
	if CrashedInLog("") {
		t.Error("empty log reported a crash")
	}
}

func TestCrashReason(t *testing.T) {
	cases := map[string]string{
		// Go slog: the msg, not the whole line.
		"[INFO] Starting vikunja...\n" +
			`time=2026-10-08T21:49:32Z level=INFO msg="No config file found"` + "\n" +
			`time=2026-10-08T21:49:32Z level=ERROR msg="service.publicurl must include http:// or https:// scheme, got: f/"` + "\n" +
			"[s6] Service 'vikunja' crashed (Exit: 1, Signal: 0)\n[s6] Waiting 5 seconds before restart...": "service.publicurl must include http:// or https:// scheme, got: f/",
		// A Python traceback's last error line.
		"Traceback (most recent call last):\n  File \"x.py\"\nModuleNotFoundError: No module named 'opentelemetry'\n[s6] Service 'ml' crashed (Exit: 3, Signal: 0)": "ModuleNotFoundError: No module named 'opentelemetry'",
		// Nothing the app said: no reason, the caller says "see Logs".
		"[INFO] starting\n[s6] Service 'x' crashed (Exit: 1, Signal: 0)": "",
	}
	for tail, want := range cases {
		if got := CrashReason(tail); got != want {
			t.Errorf("CrashReason(%q) = %q, want %q", tail, got, want)
		}
	}
}
