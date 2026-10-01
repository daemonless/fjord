package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A stack's action output is kept, bounded from the end at a line start,
// marked done when the action ends, and served to any page that asks.
func TestActionOutput(t *testing.T) {
	s := &server{}
	b := outputs.start("app", "update")
	b.Write([]byte("$ podman compose pull\n"))
	b.Write([]byte("Getting image source signatures\n"))
	v, ok := outputs.get("app")
	if !ok || v.Action != "update" || v.Done || !strings.HasPrefix(v.Text, "$ podman compose pull\n") {
		t.Errorf("mid-action: %+v", v)
	}
	b.finish()
	w := httptest.NewRecorder()
	s.stackOutput(w, "app")
	var got outputView
	json.Unmarshal(w.Body.Bytes(), &got)
	if !got.Done || got.Text != v.Text {
		t.Errorf("served: %+v", got)
	}
	// Nothing recorded yet is an empty, finished answer, not an error.
	w = httptest.NewRecorder()
	s.stackOutput(w, "other")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"done":true`) {
		t.Errorf("unknown stack: %d %s", w.Code, w.Body.String())
	}
	// Bounded: the last outputKeep bytes, starting at a line.
	big := outputs.start("app", "install")
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < outputKeep/100+50; i++ {
		big.Write([]byte(line))
	}
	v, _ = outputs.get("app")
	if len(v.Text) > outputKeep || !strings.HasPrefix(v.Text, "x") || strings.HasPrefix(v.Text, "\n") {
		t.Errorf("bound: %d bytes, starts %q", len(v.Text), v.Text[:1])
	}
}
