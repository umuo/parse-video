package cmd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wujunwei928/parse-video/parser"
)

const downloadTestHost = "http://media.example.net"

func newDownloadTestClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	if err := parser.InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parser.InitProxy("") })
	client := parser.NewMediaClient([]string{downloadTestHost})
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestDownloadFilePreservesExistingDestination(t *testing.T) {
	var requests atomic.Int32
	client := newDownloadTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	filename := filepath.Join(t.TempDir(), "existing.mp4")
	if err := os.WriteFile(filename, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadFile(context.Background(), client, downloadTestHost+"/missing", filename); !errors.Is(err, fs.ErrExist) {
		t.Errorf("expected a file conflict, got %v", err)
	}
	content, err := os.ReadFile(filename)
	if err != nil || string(content) != "keep me" {
		t.Fatalf("existing destination changed: %q, %v", content, err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("conflict made %d network requests", got)
	}
}

func TestDownloadFileCleansUpFailedDownloads(t *testing.T) {
	client := newDownloadTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/truncated":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, "partial")
		}
	}))
	for _, test := range []struct {
		path, message string
	}{
		{"/missing", "HTTP 404"},
		{"/truncated", "unexpected EOF"},
		{"/empty", "空媒体文件"},
	} {
		t.Run(test.path, func(t *testing.T) {
			dir := t.TempDir()
			err := downloadFile(context.Background(), client, downloadTestHost+test.path, filepath.Join(dir, "video.mp4"))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q, got %v", test.message, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed download left files: %v, %v", entries, err)
			}
		})
	}
}

func TestConcurrentDownloadsPublishOnlyOneCompleteFile(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	client := newDownloadTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "complete "+r.URL.Path)
	}))
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	dir := t.TempDir()
	destination := filepath.Join(dir, "video.mp4")
	results := make(chan error, 2)
	for _, mediaPath := range []string{"/first", "/second"} {
		go func() {
			results <- downloadFile(context.Background(), client, downloadTestHost+mediaPath, destination)
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("both requests did not reach the server")
		}
	}
	unblock()
	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}
	if first != nil || !errors.Is(second, fs.ErrExist) {
		t.Fatalf("expected one success and one conflict, got %v and %v", first, second)
	}
	content, err := os.ReadFile(destination)
	if err != nil || (string(content) != "complete /first" && string(content) != "complete /second") {
		t.Fatalf("unexpected published content %q, %v", content, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected only the published file, got %v, %v", entries, err)
	}
}

func TestDownloadFilenamesUseControlledExtensions(t *testing.T) {
	for _, test := range []struct {
		url, fallback, want string
	}{
		{"https://media.example.net/a.WEBP?token=value", ".jpg", ".webp"},
		{"https://media.example.net/a.exe", ".mp4", ".mp4"},
		{"https://media.example.net/a.png%3Astream", ".jpg", ".jpg"},
		{"https://media.example.net/a.jpg%5Cother", ".jpg", ".jpg"},
		{"https://media.example.net/a.svg", ".jpg", ".jpg"},
		{"https://media.example.net/a.m3u8", ".mp4", ".m3u8"},
	} {
		if got := mediaExtension(test.url, test.fallback); got != test.want {
			t.Errorf("mediaExtension(%q) = %q, want %q", test.url, got, test.want)
		}
	}
	name := sanitizeFilename(strings.Repeat("长标题", 100))
	if len(name) > 200 || !utf8.ValidString(name) || !filepath.IsLocal(name+".mp4") {
		t.Errorf("invalid filename %q", name)
	}
}
