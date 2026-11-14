package parser

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
)

// resetProxy 清理代理状态，防止测试间互相污染
func resetProxy() {
	proxyURL.Store("")
}

func TestInitProxy_EmptyString(t *testing.T) {
	t.Cleanup(resetProxy)
	resetProxy()
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8080")

	err := InitProxy("")
	if err != nil {
		t.Fatalf("InitProxy(\"\") 不应报错, got: %v", err)
	}

	client := newClient(SourceAcFun)
	if client.IsProxySet() {
		t.Fatal("未设置代理时, client 不应携带代理配置")
	}
	if client.GetClient().Transport.(checkedTransport).Proxy != nil {
		t.Fatal("未显式配置代理时不应使用环境代理")
	}
}

func TestInitProxy_InvalidURL(t *testing.T) {
	t.Cleanup(resetProxy)
	resetProxy()

	for _, proxy := range []string{"://invalid", "example.com", "socks5://proxy.example.com:1080"} {
		if err := InitProxy(proxy); err == nil {
			t.Errorf("无效代理地址 %q 应返回 error", proxy)
		}
	}
}

func TestInitProxy_ValidURL(t *testing.T) {
	t.Cleanup(resetProxy)
	resetProxy()

	err := InitProxy("http://proxy.example.com:8080")
	if err != nil {
		t.Fatalf("合法代理地址不应报错, got: %v", err)
	}

	client := newClient(SourceAcFun)
	if !client.IsProxySet() {
		t.Fatal("设置代理后, client 应携带代理配置")
	}
}

func TestProxyFailureDoesNotDialTarget(t *testing.T) {
	const proxyAddress = "127.0.0.1:8899"
	proxyErr := errors.New("test proxy is unavailable")
	directErr := errors.New("unexpected direct connection")
	t.Cleanup(resetProxy)

	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			if err := InitProxy(scheme + "://" + proxyAddress); err != nil {
				t.Fatal(err)
			}
			client := newClient(SourceAcFun)
			var proxyAttempts, directAttempts atomic.Int32
			transport := client.GetClient().Transport.(checkedTransport).Transport
			transport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
				if address != proxyAddress {
					directAttempts.Add(1)
					return nil, directErr
				}
				proxyAttempts.Add(1)
				return nil, proxyErr
			}

			_, err := client.R().Get("http://www.acfun.cn/video")
			if !errors.Is(err, proxyErr) || proxyAttempts.Load() != 1 || directAttempts.Load() != 0 {
				t.Fatalf("expected one failed proxy dial and no direct dials, got proxy=%d, direct=%d, error=%v",
					proxyAttempts.Load(), directAttempts.Load(), err)
			}
		})
	}
}

func TestNewClient_Concurrent(t *testing.T) {
	t.Cleanup(resetProxy)
	resetProxy()

	_ = InitProxy("http://proxy.example.com:8080")

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := newClient(SourceAcFun)
			if !client.IsProxySet() {
				t.Errorf("goroutine %d: client 应携带代理配置", i)
			}
		}()
	}
	wg.Wait()
}

func TestClientLimitsDecompressedResponse(t *testing.T) {
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err := zipper.Write(bytes.Repeat([]byte("x"), maxParseResponseBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	_, err := ParseVideoShareUrl("http://www.acfun.cn/video")
	if !errors.Is(err, resty.ErrResponseBodyTooLarge) {
		t.Fatalf("expected decompressed response size error, got %v", err)
	}
}

func TestParseBudgetPreservesEarlierCallerDeadline(t *testing.T) {
	callerDeadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()

	checkDeadline := func(want time.Time) func(context.Context, string) (*VideoParseInfo, error) {
		return func(ctx context.Context, _ string) (*VideoParseInfo, error) {
			got, ok := ctx.Deadline()
			if !ok || got.After(want) || got.Before(want.Add(-time.Second)) {
				t.Errorf("unexpected parse deadline %v, want %v", got, want)
			}
			return &VideoParseInfo{}, nil
		}
	}
	if _, err := parseWithTimeout(ctx, checkDeadline(callerDeadline), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := parseWithTimeout(context.Background(), checkDeadline(time.Now().Add(parseTimeout+time.Second)), ""); err != nil {
		t.Fatal(err)
	}
}
