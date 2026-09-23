package updates

import "testing"

// Real version labels from jupiter's images, 2026-09-23.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     Class
	}{
		{"10.6.106", "10.6.106", Rebuild},               // unifi's pending update: same version, rebuilt
		{"2.9.0_2", "2.9.0_3", Rebuild},                 // smokeping: FreeBSD port revision only
		{"1.30.4-r1-ls376", "1.30.4-r1-ls377", Rebuild}, // linuxserver build suffix
		{"1.4", "1.4.0", Rebuild},                       // pkg-cache: missing part is 0
		{"2.0.5", "2.0.6", Patch},                       // openspeedtest
		{"v2.18.1", "v2.19.0", Minor},                   // tautulli, v prefix
		{"v1.2.3", "2.0.0", Major},                      // readmeabook, prefix on one side only
		{"6.4.4.10685", "6.4.4.10700", Patch},           // radarr: 4th part is a build number
		{"4.0.20.3014", "4.1.0.3100", Minor},            // sonarr
		{"1.102.4", "2.0.0", Major},                     // tailscale
		{"2026-05-19", "2026-06-02", Unknown},           // organizr: a date has no major/minor
		{"", "1.2.3", Unknown},                          // no label on the running image
		{"latest", "stable", Unknown},                   // nothing numeric
	} {
		if got := Classify(tc.from, tc.to); got != tc.want {
			t.Errorf("Classify(%q, %q) = %s, want %s", tc.from, tc.to, got, tc.want)
		}
	}
}
