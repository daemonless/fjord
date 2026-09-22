package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A mount change is an edit the client holds until Save, so the response has
// to carry everything the client needs to show it. It used to return only the
// compose; the client then re-read the stack to get the per-service Storage
// list, and the re-read -- being what is on DISK -- replaced the edit with the
// unchanged file. Adding a bind mount looked like it did nothing at all.
func TestComposeMountsAddReturnsServicesForTheNewCompose(t *testing.T) {
	const compose = `services:
  app:
    image: nginx
    volumes:
      - /srv/data:/data
`
	body := `{"op":"add","kind":"bind","source":"/mnt","dest":"/test","compose":` +
		mustJSON(t, compose) + `,"env":""}`

	rec := httptest.NewRecorder()
	(&server{}).handleComposeMounts(rec, httptest.NewRequest(http.MethodPost, "/api/compose/mounts", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Compose  string `json:"compose"`
		Services []struct {
			Name    string `json:"name"`
			Volumes []struct {
				Source string `json:"source"`
				Dest   string `json:"dest"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(got.Compose, "/mnt:/test") {
		t.Errorf("compose does not carry the new mount:\n%s", got.Compose)
	}
	if len(got.Services) != 1 {
		t.Fatalf("want 1 service, got %d", len(got.Services))
	}
	var found bool
	for _, v := range got.Services[0].Volumes {
		if v.Source == "/mnt" && v.Dest == "/test" {
			found = true
		}
	}
	if !found {
		t.Errorf("service %q does not report the new mount: %+v", got.Services[0].Name, got.Services[0].Volumes)
	}
}

func TestComposeMountsRemoveReturnsServicesForTheNewCompose(t *testing.T) {
	const compose = `services:
  app:
    image: nginx
    volumes:
      - /srv/data:/data
      - /mnt:/test
`
	body := `{"op":"remove","dest":"/test","compose":` + mustJSON(t, compose) + `,"env":""}`

	rec := httptest.NewRecorder()
	(&server{}).handleComposeMounts(rec, httptest.NewRequest(http.MethodPost, "/api/compose/mounts", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Compose  string `json:"compose"`
		Services []struct {
			Volumes []struct {
				Dest string `json:"dest"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(got.Compose, "/mnt:/test") {
		t.Errorf("compose still carries the removed mount:\n%s", got.Compose)
	}
	if len(got.Services) != 1 {
		t.Fatalf("want 1 service, got %d", len(got.Services))
	}
	for _, v := range got.Services[0].Volumes {
		if v.Dest == "/test" {
			t.Errorf("service still reports the removed mount: %+v", got.Services[0].Volumes)
		}
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
