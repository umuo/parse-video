package parser

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMediaClientChecksInitialURLsAndRedirects(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/sibling":
			http.Redirect(w, r, "http://cdn.example.net/final", http.StatusFound)
		case "/other-media":
			http.Redirect(w, r, "http://cdn.example.com/final", http.StatusFound)
		case "/private":
			http.Redirect(w, r, "http://[::ffff:127.0.0.1]/final", http.StatusFound)
		case "/outside":
			http.Redirect(w, r, "http://example.org/final", http.StatusFound)
		case "/other-tenant":
			http.Redirect(w, r, "http://other.github.io/final", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}

	client := NewMediaClient([]string{"http://media.example.net/sibling", "http://cdn.example.com/image", "http://media.github.io/image"})
	t.Cleanup(client.CloseIdleConnections)
	for _, test := range []struct {
		url     string
		allowed bool
		hits    int32
	}{
		{"http://media.example.net/sibling", true, 2},
		{"http://media.example.net/other-media", true, 2},
		{"http://media.example.net/private", false, 1},
		{"http://media.example.net/outside", false, 1},
		{"http://media.github.io/other-tenant", false, 1},
		{"http://127.0.0.1/initial", false, 0},
		{"http://user@media.example.net/initial", false, 0},
		{"http://media.example.net:8080/initial", false, 0},
		{"file:///media.example.net/initial", false, 0},
	} {
		t.Run(test.url, func(t *testing.T) {
			requests.Store(0)
			response, err := client.Get(test.url)
			if response != nil {
				_ = response.Body.Close()
			}
			if test.allowed && (err != nil || response == nil || response.StatusCode != http.StatusOK) {
				t.Errorf("allowed request failed: %v", err)
			}
			if !test.allowed && !errors.Is(err, errUntrustedTarget) {
				t.Errorf("expected target rejection, got %v", err)
			}
			if count := requests.Load(); count != test.hits {
				t.Errorf("proxy got %d requests, want %d", count, test.hits)
			}
		})
	}
}
