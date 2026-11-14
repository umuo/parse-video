package parser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestShareURLRejectsUntrustedDestinations(t *testing.T) {
	var blockedHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockedHits.Add(1)
		fmt.Fprint(w, `var videoInfo = {"title":"internal"};`)
	}))
	t.Cleanup(target.Close)

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		switch r.URL.Path {
		case "/video":
			fmt.Fprint(w, `var videoInfo = {"title":"public"};`)
		case "/allowed":
			http.Redirect(w, r, "http://www.acfun.cn/video", http.StatusFound)
		case "/kuaishou":
			http.Redirect(w, r, "http://www.kuaishou.com/fw/long-video/example", http.StatusFound)
		case "/fw/photo/example":
			fmt.Fprint(w, `window.INIT_STATE = {"item":{"result":1,"photo":{"caption":"public"}}}</script>`)
		case "/redirect", "/manual":
			http.Redirect(w, r, target.URL+"/private", http.StatusFound)
		default:
			blockedHits.Add(1)
			fmt.Fprint(w, `var videoInfo = {"title":"internal"};`)
		}
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}

	for _, shareURL := range []string{
		"http://www.acfun.cn/video",
		"http://www.acfun.cn/allowed",
		"http://v.kuaishou.com/kuaishou",
	} {
		t.Run(shareURL, func(t *testing.T) {
			info, err := ParseVideoShareUrl(shareURL)
			if err != nil || info == nil || info.Title != "public" {
				t.Fatalf("got %+v, %v", info, err)
			}
		})
	}

	for _, test := range []struct {
		name string
		url  string
		via  int32
	}{
		{"platform in path", target.URL + "/www.acfun.cn", 0},
		{"hostname suffix spoof", "http://www.acfun.cn.example.net/video", 0},
		{"credentials in authority", "http://user@www.acfun.cn/video", 0},
		{"platform in userinfo", "http://www.acfun.cn@127.0.0.1/video", 0},
		{"unexpected port", "http://www.acfun.cn:8080/video", 0},
		{"automatic redirect", "http://www.acfun.cn/redirect", 1},
		{"manual redirect", "http://v.kuaishou.com/manual", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxyHits.Store(0)
			blockedHits.Store(0)
			if _, err := ParseVideoShareUrlByRegexp(test.url); err == nil {
				t.Error("expected an error")
			}
			if got := proxyHits.Load(); got != test.via {
				t.Errorf("proxy got %d requests, want %d", got, test.via)
			}
			if got := blockedHits.Load(); got != 0 {
				t.Errorf("forbidden target got %d requests", got)
			}
		})
	}
}

func TestPublicAddress(t *testing.T) {
	for _, host := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1",
		"192.168.0.1", "198.18.0.1", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1",
		"64:ff9b::a00:1", "2002:a00:1::", "fc00::1", "fe80::1", "3fff::1",
	} {
		if publicAddress(netip.MustParseAddr(host)) {
			t.Errorf("allowed special-purpose address %s", host)
		}
	}
	for _, host := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(host)) {
			t.Errorf("rejected public address %s", host)
		}
	}
}

func TestBilibiliShortLinkRedirects(t *testing.T) {
	var loopHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loop" {
			loopHits.Add(1)
			http.Redirect(w, r, "/loop", http.StatusFound)
			return
		}
		http.Redirect(w, r, "http://www.bilibili.com/video/BV1xx411c7mD", http.StatusFound)
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	if id, err := (biliBili{}).getBvidFromURL(context.Background(), "http://b23.tv/share"); id != "BV1xx411c7mD" || err != nil {
		t.Fatalf("unexpected short-link result %q, %v", id, err)
	}
	if _, err := (biliBili{}).getBvidFromURL(context.Background(), "http://b23.tv/loop"); err == nil || loopHits.Load() != maxRedirects {
		t.Fatalf("loop got %d requests, error %v", loopHits.Load(), err)
	}
}

func TestShortLinkWithoutRedirectReturnsError(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing-location" {
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"v.douyin.com", "share.huoshan.com", "h5.pipix.com", "v.ixigua.com"} {
		for _, path := range []string{"/ok", "/missing-location"} {
			t.Run(host+path, func(t *testing.T) {
				info, err := ParseVideoShareUrl("http://" + host + path)
				if info != nil || !errors.Is(err, errShortLinkDidNotRedirect) {
					t.Fatalf("expected missing redirect error and no result, got result=%t, error=%v", info != nil, err)
				}
			})
		}
	}
}

func TestRedirectStripsCredentialsAcrossHosts(t *testing.T) {
	var stripped atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "www.acfun.cn" {
			http.Redirect(w, r, "http://mobile.www.acfun.cn/final", http.StatusFound)
			return
		}
		stripped.Store(r.Header.Get("Cookie") == "" && r.Header.Get("Authorization") == "" && r.Referer() == "")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	_, err := newClient(SourceAcFun).R().
		SetHeader("Cookie", "test=placeholder").
		SetHeader("Authorization", "Bearer placeholder").
		Get("http://www.acfun.cn/video")
	if err != nil || !stripped.Load() {
		t.Fatalf("credentials were not stripped on redirect, error %v", err)
	}
}

func TestRedirectStripsRefererOnReturnToInitialHost(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://mobile.www.acfun.cn/intermediate?token=placeholder", http.StatusFound)
		case "/intermediate":
			http.Redirect(w, r, "http://www.acfun.cn/final", http.StatusFound)
		case "/final":
			if r.Referer() != "" {
				t.Error("cross-host Referer survived the redirect back to the initial host")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	response, err := newClient(SourceAcFun).R().Get("http://www.acfun.cn/start")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusNoContent {
		t.Fatalf("expected to reach the final URL, got HTTP %d", response.StatusCode())
	}
}

func TestPublicDialContext(t *testing.T) {
	public := netip.MustParseAddr("8.8.8.8")
	private := netip.MustParseAddr("::ffff:127.0.0.1")
	for _, test := range []struct {
		name      string
		addresses []netip.Addr
		wantDial  bool
	}{
		{"pins the resolved public address", []netip.Addr{public}, true},
		{"rejects mixed public and private answers", []netip.Addr{public, private}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			dialErr := errors.New("test dial failure")
			dial := publicDialContext(
				func(_ context.Context, network, host string) ([]netip.Addr, error) {
					if network != "ip" || host != "www.acfun.cn" {
						t.Fatalf("unexpected lookup %s %s", network, host)
					}
					return test.addresses, nil
				},
				func(_ context.Context, network, address string) (net.Conn, error) {
					dialed = true
					if network != "tcp" || address != "8.8.8.8:443" {
						t.Fatalf("dialed %s %s instead of the validated IP", network, address)
					}
					return nil, dialErr
				},
			)
			_, err := dial(context.Background(), "tcp", "www.acfun.cn:443")
			if err == nil || dialed != test.wantDial || errors.Is(err, dialErr) != test.wantDial {
				t.Fatalf("dialed %v, got error %v", dialed, err)
			}
		})
	}
}
