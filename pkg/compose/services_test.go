package compose

import "testing"

// A named volume is storage the service holds. Dropping it left a service
// whose ONLY storage is one -- immich's model cache, redis's data dir --
// reporting no storage at all on the page that exists to show it.
func TestParseServicesNamedVolumes(t *testing.T) {
	svcs := ParseServices(`services:
  ml:
    image: a
    volumes:
      - model-cache:/cache
  redis:
    image: b
    volumes:
      - /etc/localtime:/etc/localtime:ro
      - redis-data:/config
  odd:
    image: c
    volumes:
      - ./relative:/x
      - notapath
      - ${UNSET}:/y
volumes:
  model-cache:
  redis-data:
`, nil)
	by := map[string][]VolMount{}
	for _, s := range svcs {
		by[s.Name] = s.Volumes
	}
	if v := by["ml"]; len(v) != 1 || v[0].Name != "model-cache" || v[0].Dest != "/cache" || v[0].Source != "" {
		t.Fatalf("ml: %+v", v)
	}
	// Binds keep working, read-only included, and both kinds coexist.
	v := by["redis"]
	if len(v) != 2 || v[0].Source != "/etc/localtime" || !v[0].ReadOnly || v[1].Name != "redis-data" {
		t.Fatalf("redis: %+v", v)
	}
	// A relative path, a bare word with no container side, and an unresolved
	// variable are none of them volumes.
	if v := by["odd"]; len(v) != 0 {
		t.Errorf("odd: %+v, want nothing", v)
	}
}
