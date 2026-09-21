package appjail

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
	if crashedInLog(restartedLog) {
		t.Error("a SIGTERM stop was read as a crash")
	}
	if !crashedInLog(faultingLog) {
		t.Error("a real crash loop was missed")
	}
	if crashedInLog("") {
		t.Error("empty log reported a crash")
	}
}
