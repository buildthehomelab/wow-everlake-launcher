package cdn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

func TestHostSchemes(t *testing.T) {
	k := tact.MustKey("0123456789abcdef0123456789abcdef")
	c := New(nil, "tpr/wow")
	for host, want := range map[string]string{
		"level3.blizzard.com":           "http://level3.blizzard.com/tpr/wow/config/01/23/" + k.String(),
		"10.0.0.5:8088":                 "http://10.0.0.5:8088/tpr/wow/config/01/23/" + k.String(),
		"https://cdn.example.com":       "https://cdn.example.com/tpr/wow/config/01/23/" + k.String(),
		"https://cdn.example.com:8443/": "https://cdn.example.com:8443/tpr/wow/config/01/23/" + k.String(),
		"http://cdn.example.com":        "http://cdn.example.com/tpr/wow/config/01/23/" + k.String(),
	} {
		if got := c.URL(host, "config", k, ""); got != want {
			t.Errorf("%s: %s, want %s", host, got, want)
		}
	}
}

func TestFetchOverHTTPS(t *testing.T) {
	body := []byte("a config")
	k := tact.KeyOf(body)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tpr/wow/config/"+k.String()[:2]+"/"+k.String()[2:4]+"/"+k.String() {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	c := New([]string{srv.URL}, "tpr/wow")
	c.HTTP = srv.Client() // trusts the test certificate
	got, err := c.Get(context.Background(), "config", k, "")
	if err != nil || string(got) != string(body) {
		t.Fatalf("got %q, %v", got, err)
	}
}
