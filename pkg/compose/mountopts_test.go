package compose

import (
	"reflect"
	"strings"
	"testing"
)

func TestMountOptionsSplit(t *testing.T) {
	for in, want := range map[string][]string{
		"": nil, "ro": {"ro"}, "ro,Z": {"ro", "Z"}, " z , ro,z": {"z", "ro"},
	} {
		if got := MountOptions(in); !reflect.DeepEqual(got, want) {
			t.Errorf("MountOptions(%q) = %v, want %v", in, got, want)
		}
	}
}

// What podman would refuse at start is refused at the edit, per host: on
// FreeBSD podman takes z and Z but not O.
func TestCheckMountOptions(t *testing.T) {
	ok := [][]string{nil, {"ro"}, {"ro", "z"}, {"Z"}, {"U", "nocopy"}, {"idmap=uids=0-1000-10"}}
	for _, o := range ok {
		if err := CheckMountOptions(o, "linux"); err != nil {
			t.Errorf("%v on linux: %v", o, err)
		}
	}
	if err := CheckMountOptions([]string{"z", "U"}, "freebsd"); err != nil {
		t.Errorf("z,U on freebsd: %v", err)
	}
	bad := map[string][]string{
		"linux":   {"bogus"},
		"freebsd": {"O"},
	}
	for goos, o := range bad {
		if err := CheckMountOptions(o, goos); err == nil {
			t.Errorf("%v on %s: no error", o, goos)
		}
	}
	for _, o := range [][]string{{"ro", "rw"}, {"z", "Z"}} {
		if err := CheckMountOptions(o, "linux"); err == nil {
			t.Errorf("%v: no error", o)
		}
	}
	if err := CheckMountOptions([]string{"O"}, "linux"); err != nil {
		t.Errorf("O on linux: %v", err)
	}
}

const optsBase = `services:
  app:
    image: nginx
    volumes:
      - ${CONFIG_DATA}:/config
      - "/mars:/mars:ro"
`

// The options change; the source stays as written, a ${VAR} included.
func TestSetMountOptions(t *testing.T) {
	out, err := SetMountOptions(optsBase, "app", "/config", []string{"z"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"${CONFIG_DATA}:/config:z"`) {
		t.Errorf("options not set:\n%s", out)
	}
	out, err = SetMountOptions(out, "app", "/mars", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"/mars:/mars"`) || strings.Contains(out, "/mars:ro") {
		t.Errorf("options not cleared:\n%s", out)
	}
	if _, err := SetMountOptions(optsBase, "app", "/nothing", nil); err == nil {
		t.Error("no mount at /nothing: no error")
	}
}

func TestMountsCarryOptions(t *testing.T) {
	ms := Mounts(`services:
  app:
    image: nginx
    volumes:
      - /a:/a:ro,Z
      - /b:/b:nocopy
`, nil)
	if len(ms) != 2 {
		t.Fatalf("mounts %+v", ms)
	}
	if !reflect.DeepEqual(ms[0].Options, []string{"ro", "Z"}) || !ms[0].ReadOnly {
		t.Errorf("/a: %+v", ms[0])
	}
	if !reflect.DeepEqual(ms[1].Options, []string{"nocopy"}) || ms[1].ReadOnly {
		t.Errorf("/b: %+v", ms[1])
	}
	out, err := AttachBindMount(optsBase, "app", "/srv", "/srv", []string{"ro", "z"})
	if err != nil || !strings.Contains(out, `"/srv:/srv:ro,z"`) {
		t.Errorf("attach with options: %v\n%s", err, out)
	}
}
