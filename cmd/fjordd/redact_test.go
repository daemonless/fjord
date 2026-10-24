package main

import "testing"

func TestRedactSecrets(t *testing.T) {
	in := "# app\nWEB_PORT=8080\nTAG=1.2\nADMIN_PASSWORD=hunter2hunter2\nexport API_TOKEN=abc\nWG_PRIVATE_KEY = xyz\nPUID=1000\n"
	want := "# app\nWEB_PORT=8080\nTAG=1.2\nADMIN_PASSWORD=\nAPI_TOKEN=\nWG_PRIVATE_KEY=\nPUID=1000\n"
	if got := redactSecrets(in); got != want {
		t.Errorf("redactSecrets:\n%q\nwant\n%q", got, want)
	}
}
