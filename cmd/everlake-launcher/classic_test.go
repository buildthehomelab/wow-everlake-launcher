package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchOverlayManifest(t *testing.T) {
	const manifest = `{"version":"1.60.1.70790"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/everlake/build.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(manifest))
	}))
	defer server.Close()

	data, err := fetchOverlayManifest(context.Background(), server.URL+"/")
	if err != nil || string(data) != manifest {
		t.Fatalf("fetchOverlayManifest = %q, %v", data, err)
	}
	if _, err := fetchOverlayManifest(context.Background(), server.URL+"/wrong"); err == nil {
		t.Fatal("accepted a mirror URL with a path")
	}
}
