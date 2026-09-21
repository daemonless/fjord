package appjail

import "testing"

// The warning appjail printed when zensical would not delete. The jail name has
// to come out of it, because that is the only place the failing jail is named.
const busyOutput = `Stopping zensical (zensical_zensical) ... Done.
Destroying zensical (zensical_zensical) ... FAIL!
umount: unmount of /usr/local/appjail/jails/zensical_zensical/jail/config failed: Device busy
 [ warn  ] [zensical_zensical] The jail directory (/usr/local/appjail/jails/zensical_zensical) has one or more mounted file systems:
 [ warn  ] [zensical_zensical]     - /var/db/fjord/containers/zensical/config -> /usr/local/appjail/jails/zensical_zensical/jail/config
`

func TestJailsInOutput(t *testing.T) {
	got := jailsInOutput(busyOutput)
	if len(got) != 1 || got[0] != "zensical_zensical" {
		t.Fatalf("got %v, want [zensical_zensical]", got)
	}
	// Named once however many lines mention it.
	if n := len(jailsInOutput(busyOutput + busyOutput)); n != 1 {
		t.Errorf("got %d names, want 1", n)
	}
	if got := jailsInOutput("nothing to see"); got != nil {
		t.Errorf("got %v, want nothing", got)
	}
}

// Only this failure is retried. An unrelated one must surface as it is rather
// than being followed by a forced unmount of a jail that is fine.
func TestBusyMountMarker(t *testing.T) {
	for out, want := range map[string]bool{
		busyOutput:                           true,
		"Destroying ... FAIL!\nno such jail": false,
		"":                                   false,
	} {
		if got := containsBusyMount(out); got != want {
			t.Errorf("containsBusyMount(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestForceUnmountRefusesAName(t *testing.T) {
	for _, bad := range []string{"", "a jail", "x\ty"} {
		if _, err := forceUnmountJail(t.Context(), bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
