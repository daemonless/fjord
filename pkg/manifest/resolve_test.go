package manifest

import "testing"

// Every optional value left empty is reported, not only ports, so the
// install can leave the setting out instead of setting it to nothing.
func TestResolveEmptyOptionalStrings(t *testing.T) {
	m, err := Parse(`services:
  app:
    image: x
    environment:
      - DB_TYPE=${DB_TYPE}
x-fjord:
  info:
    id: app
  variables:
    - name: DB_TYPE
      type: string
      optional: true
    - name: NAME
      type: string
      default: x
`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Resolve(map[string]string{}, "app", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.EmptyOptional) != 1 || res.EmptyOptional[0] != "DB_TYPE" {
		t.Errorf("EmptyOptional %v", res.EmptyOptional)
	}
}
