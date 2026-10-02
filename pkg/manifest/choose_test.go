package manifest

import (
	"strings"
	"testing"
)

const choosy = `services:
  app:
    image: ghcr.io/daemonless/app:latest
    environment:
      - APP_DB_TYPE=${APP_DB_TYPE}
      - APP_DB_HOST=${APP_DB_HOST}
      - APP_DB_PASSWORD=${APP_DB_PASSWORD}
      - ML=${ML_ENABLED}
    depends_on:
      - ml
  ml:
    image: ghcr.io/daemonless/app-ml:latest
x-fjord:
  version: "0.1"
  variables:
    - name: APP_DB_TYPE
      type: string
      default: sqlite
      optional: true
    - name: APP_DB_HOST
      type: string
      optional: true
    - name: APP_DB_PASSWORD
      type: secret
      optional: true
    - name: ML_ENABLED
      type: string
      default: "true"
      optional: true
  choices:
    - id: database
      kind: database
      label: Database
      default: sqlite
      options:
        - id: sqlite
          label: SQLite
          env: {APP_DB_TYPE: sqlite}
        - id: postgres
          label: PostgreSQL
          env: {APP_DB_TYPE: postgres, APP_DB_HOST: postgres}
          defaults: {APP_DB_USER: app, DATABASE_LOCATION: /containers/app/postgres}
          secrets: [APP_DB_PASSWORD]
          services: |
              postgres:
                image: ghcr.io/daemonless/postgres:17
                environment:
                  - POSTGRES_PASSWORD=${APP_DB_PASSWORD}
          depends_on: {app: [postgres]}
        - id: external
          label: Your own
          ask:
            - {name: APP_DB_TYPE, label: Kind, values: {postgres: PostgreSQL}}
            - {name: APP_DB_HOST, label: Host}
            - {name: APP_DB_PASSWORD, label: Password, type: secret}
    - id: machine_learning
      kind: part
      label: Machine learning
      default: "on"
      options:
        - id: "on"
          label: With
        - id: "off"
          label: Without
          drop: [ml]
          env: {ML_ENABLED: "false"}
`

func TestDefaultsAnswerEveryChoice(t *testing.T) {
	m, err := Parse(choosy)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	answers, err := m.ApplyChoices(nil, values)
	if err != nil {
		t.Fatal(err)
	}
	if answers["database"] != "sqlite" || answers["machine_learning"] != "on" {
		t.Errorf("answers %v", answers)
	}
	if values["APP_DB_TYPE"] != "sqlite" || strings.Contains(m.Compose(), "postgres:") || !strings.Contains(m.Compose(), "  ml:") {
		t.Errorf("default left the compose as it stands? values=%v compose=\n%s", values, m.Compose())
	}
}

func TestPostgresAddsTheServiceAndMakesASecret(t *testing.T) {
	m, _ := Parse(choosy)
	values := map[string]string{}
	answers, err := m.ApplyChoices(map[string]string{"database": "postgres", "machine_learning": "off"}, values)
	if err != nil {
		t.Fatal(err)
	}
	c := m.Compose()
	if !strings.Contains(c, "  postgres:\n    image: ghcr.io/daemonless/postgres:17") {
		t.Errorf("postgres not added:\n%s", c)
	}
	if !strings.Contains(c, "depends_on:\n      - postgres") || strings.Contains(c, "- ml") || strings.Contains(c, "app-ml") {
		t.Errorf("depends_on wrong:\n%s", c)
	}
	if values["APP_DB_TYPE"] != "postgres" || values["APP_DB_HOST"] != "postgres" || values["APP_DB_USER"] != "app" || values["ML_ENABLED"] != "false" {
		t.Errorf("values %v", values)
	}
	if len(values["APP_DB_PASSWORD"]) != 24 {
		t.Errorf("no secret made: %q", values["APP_DB_PASSWORD"])
	}
	if answers["machine_learning"] != "off" {
		t.Errorf("answers %v", answers)
	}
	var names []string
	for _, v := range m.Variables {
		names = append(names, v.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "DATABASE_LOCATION") {
		t.Errorf("the added service's variable is not declared: %v", names)
	}
}

func TestYourOwnDatabaseMustBeGiven(t *testing.T) {
	m, _ := Parse(choosy)
	_, err := m.ApplyChoices(map[string]string{"database": "external"}, map[string]string{"APP_DB_TYPE": "postgres"})
	if err == nil || !strings.Contains(err.Error(), "needs Host") {
		t.Errorf("want 'needs Host', got %v", err)
	}
	m, _ = Parse(choosy)
	values := map[string]string{"APP_DB_TYPE": "postgres", "APP_DB_HOST": "db.lan", "APP_DB_PASSWORD": "x"}
	if _, err := m.ApplyChoices(map[string]string{"database": "external"}, values); err != nil {
		t.Fatal(err)
	}
	for _, v := range m.Variables {
		if v.Name == "APP_DB_HOST" && v.Optional {
			t.Error("an asked value must be required")
		}
	}
	if strings.Contains(m.Compose(), "postgres:") {
		t.Error("your own database adds no service")
	}
}

func TestUnknownOptionIsRefused(t *testing.T) {
	m, _ := Parse(choosy)
	if _, err := m.ApplyChoices(map[string]string{"database": "oracle"}, map[string]string{}); err == nil {
		t.Error("want an error naming the options")
	}
}
