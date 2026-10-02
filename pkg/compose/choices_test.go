package compose

import (
	"strings"
	"testing"
)

const choicesBase = `services:
  app:
    image: ghcr.io/daemonless/app:latest
    depends_on:
      - ml
      - db
  ml:
    image: ghcr.io/daemonless/app-ml:latest
  db:
    image: ghcr.io/daemonless/postgres:17
`

func TestAddServicesAppendsAFragment(t *testing.T) {
	out, err := AddServices(choicesBase, "  proxy:\n    image: ghcr.io/daemonless/app-proxy:latest\n    depends_on:\n      - app\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  proxy:\n    image: ghcr.io/daemonless/app-proxy:latest") {
		t.Errorf("proxy not added:\n%s", out)
	}
	if _, err := AddServices(out, "  app:\n    image: x\n"); err == nil {
		t.Error("redefining a service should be refused")
	}
	if same, _ := AddServices(choicesBase, "  \n"); same != choicesBase {
		t.Error("an empty fragment changes nothing")
	}
}

func TestDropServicesCleansDependsOn(t *testing.T) {
	out, err := DropServices(choicesBase, []string{"ml", "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "app-ml") || strings.Contains(out, "- ml") {
		t.Errorf("ml still there:\n%s", out)
	}
	if !strings.Contains(out, "depends_on:\n      - db") {
		t.Errorf("db dependency lost:\n%s", out)
	}
}

func TestAddDependsOnKeepsWhatIsThere(t *testing.T) {
	out, err := AddDependsOn(choicesBase, "app", []string{"db", "ml"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "- db") != 1 {
		t.Errorf("db listed twice:\n%s", out)
	}
	if _, err := AddDependsOn(choicesBase, "app", []string{"ghost"}); err == nil {
		t.Error("a dependency on a service that does not exist should be refused")
	}
	fresh := "services:\n  app:\n    image: x\n  db:\n    image: y\n"
	out, _ = AddDependsOn(fresh, "app", []string{"db"})
	if !strings.Contains(out, "    depends_on:\n      - db") {
		t.Errorf("depends_on not created:\n%s", out)
	}
}
