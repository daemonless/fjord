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

// A names provider's pairing values are empty at Resolve and filled by fjord
// after: "required value RP_PAIRING_URL is empty" refused the proxy install
// on jupiter (2026-10-26).
func TestResolveLeavesPairingToFjord(t *testing.T) {
	m := &Manifest{
		Names:     &NamesProvider{ClientURL: "${RP_PAIRING_URL}", ClientID: "${RP_PAIRING_ID}"},
		Variables: []Var{{Name: "RP_PAIRING_URL", Type: "string"}, {Name: "RP_PAIRING_ID", Type: "string"}, {Name: "OTHER", Type: "string"}},
	}
	if _, err := m.Resolve(map[string]string{"OTHER": "x"}, "rp", t.TempDir()); err != nil {
		t.Fatalf("pairing values refused: %v", err)
	}
	if _, err := m.Resolve(map[string]string{}, "rp", t.TempDir()); err == nil {
		t.Fatal("an ordinary required value left empty must still be refused")
	}
}
