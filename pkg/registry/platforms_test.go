package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An index lists its platforms; a plain manifest names one in its config
// blob; attestation entries (unknown/unknown) are not platforms.
func TestPlatforms(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/lib/app/manifests/multi":
			w.Write([]byte(`{"manifests":[
				{"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"freebsd","architecture":"amd64"}},
				{"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"freebsd","architecture":"arm64"}},
				{"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"unknown","architecture":"unknown"}}]}`))
		case "/v2/lib/app/manifests/one":
			w.Write([]byte(`{"config":{"digest":"sha256:cfg"},"layers":[]}`))
		case "/v2/lib/app/blobs/sha256:cfg":
			w.Write([]byte(`{"os":"freebsd","architecture":"amd64","config":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()
	image := strings.TrimPrefix(srv.URL, "https://") + "/lib/app"

	got, err := Platforms(context.Background(), image, "multi")
	if err != nil {
		t.Fatal(err)
	}
	if want := "freebsd/amd64,freebsd/arm64"; strings.Join(got, ",") != want {
		t.Errorf("index: %v, want %s", got, want)
	}
	got, err = Platforms(context.Background(), image, "one")
	if err != nil {
		t.Fatal(err)
	}
	if want := "freebsd/amd64"; strings.Join(got, ",") != want {
		t.Errorf("plain manifest: %v, want %s", got, want)
	}
	if _, err := Platforms(context.Background(), image, "missing"); err == nil {
		t.Error("a missing tag should be an error, not an empty list")
	}
}

func TestRunsHere(t *testing.T) {
	if !RunsHere(nil) {
		t.Error("no platform named must not read as a refusal")
	}
	if !RunsHere([]string{"linux/s390x", HostPlatform()}) {
		t.Error("this host listed, refused")
	}
	if RunsHere([]string{"linux/s390x"}) {
		t.Error("only another platform, accepted")
	}
}
