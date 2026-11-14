package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"github.com/wujunwei928/parse-video/parser"
)

func stubParserFuncs(t *testing.T,
	share func(string) (*parser.VideoParseInfo, error),
	id func(string, string) (*parser.VideoParseInfo, error),
) {
	oldShare := parseVideoShareURL
	oldID := parseVideoID
	t.Cleanup(func() {
		parseVideoShareURL = oldShare
		parseVideoID = oldID
	})
	if share != nil {
		parseVideoShareURL = func(_ context.Context, input string) (*parser.VideoParseInfo, error) { return share(input) }
	}
	if id != nil {
		parseVideoID = func(_ context.Context, source, input string) (*parser.VideoParseInfo, error) {
			return id(source, input)
		}
	}
}

func setupTestRouter(t *testing.T, auth bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(recoveryMiddleware())
	r.Use(corsMiddleware("*"))
	r.Use(requestLogMiddleware())
	limiter := newIPRateLimiterWithBurst(600, 100)
	t.Cleanup(limiter.Stop)
	r.Use(rateLimitMiddleware(limiter, "/api/v1/health"))
	if auth {
		r.Use(basicAuthMiddleware("testuser", "testpass", map[string]bool{
			"/api/v1/health":    true,
			"/api/v1/platforms": true,
			"/":                 true,
		}))
	}

	v1 := r.Group("/api/v1")
	{
		v1.GET("/health", healthHandler)
		v1.GET("/platforms", platformsHandler)
		v1.GET("/parse", v1ParseURLHandler)
		v1.GET("/parse/:source/:video_id", v1ParseIDHandler)
	}

	r.GET("/video/share/url/parse", legacyParseURLHandler)
	r.GET("/video/id/parse", legacyParseIDHandler)
	r.GET("/", func(c *gin.Context) { c.String(200, "web ui") })
	return r
}

func TestParseErrorsHideUpstreamDetails(t *testing.T) {
	const detail = "upstream returned https://example.invalid/video?sig=test-only-value"
	stubParserFuncs(t,
		func(string) (*parser.VideoParseInfo, error) { return nil, fmt.Errorf("%s", detail) },
		func(string, string) (*parser.VideoParseInfo, error) { return nil, fmt.Errorf("%s", detail) },
	)
	router := setupTestRouter(t, false)
	for _, test := range []struct {
		path   string
		status int
		body   string
	}{
		{"/api/v1/parse?url=https://www.acfun.cn/video", 422, `"code":"PARSE_FAILED"`},
		{"/api/v1/parse/acfun/123", 422, `"code":"PARSE_FAILED"`},
		{"/video/share/url/parse?url=https://www.acfun.cn/video", 200, `"code":201`},
		{"/video/id/parse?source=acfun&video_id=123", 200, `"code":201`},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.body) || strings.Contains(w.Body.String(), "test-only-value") {
			t.Errorf("%s returned HTTP %d, %s", test.path, w.Code, w.Body.String())
		}
	}
}

func TestParseCancelsUpstreamWhenClientDisconnects(t *testing.T) {
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		close(started)
		select {
		case <-req.Context().Done():
			close(upstreamCanceled)
		case <-release:
		}
	}))
	defer proxy.Close()
	if err := parser.InitProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parser.InitProxy("") })
	app := httptest.NewServer(setupTestRouter(t, false))
	defer app.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.URL+"/api/v1/parse?url=http://www.acfun.cn/video", nil)
	if err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	go func() {
		res, err := app.Client().Do(req)
		if res != nil {
			_ = res.Body.Close()
		}
		clientDone <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("parse request never reached the upstream")
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not canceled after the client disconnected")
	}
	if err := <-clientDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected client cancellation, got %v", err)
	}
}

func TestRunServeRejectsPartialBasicAuth(t *testing.T) {
	for _, values := range [][2]string{{"operator", ""}, {"", "placeholder"}} {
		t.Setenv("PARSE_VIDEO_USERNAME", values[0])
		t.Setenv("PARSE_VIDEO_PASSWORD", values[1])
		command := &cobra.Command{}
		command.Flags().String("port", "invalid", "")
		err := runServe(command, nil)
		if err == nil || !strings.Contains(err.Error(), "必须同时设置或同时留空") {
			t.Fatalf("expected startup to reject partial credentials, got %v", err)
		}
	}
}

func assertVideoParseInfoFields(t *testing.T, data map[string]any) {
	t.Helper()
	for _, key := range []string{"author", "title", "video_url", "music_url", "cover_url", "images"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data 缺少必需字段: %s", key)
		}
	}
	author := data["author"].(map[string]any)
	for _, key := range []string{"uid", "name", "avatar"} {
		if _, ok := author[key]; !ok {
			t.Errorf("author 缺少必需字段: %s", key)
		}
	}
}

func TestIntegrationHealthEndpoint(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("health 应返回 200，实际: %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Errorf("health status 应为 ok，实际: %v", resp["status"])
	}
}

func TestIntegrationPlatformsEndpoint(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/platforms", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("platforms 应返回 200，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseMissingURL(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("缺少 url 应返回 400，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseInvalidURL(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse?url=just-text-no-url", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("无有效 URL 应返回 400，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseUnknownPlatform(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse?url=https://example.com/video/123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("未知平台应返回 400，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "UNSUPPORTED_URL" {
		t.Errorf("错误码应为 UNSUPPORTED_URL，实际: %v", errObj["code"])
	}
}

func TestIntegrationV1ParseIDUnknownSource(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse/unknown_platform/123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("未知 source 应返回 400，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseIDNoIDParse(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse/redbook/123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("不支持 ID 解析应返回 400，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseURLSuccess(t *testing.T) {
	stubParserFuncs(t, func(string) (*parser.VideoParseInfo, error) {
		info := &parser.VideoParseInfo{}
		info.Title = "测试视频"
		info.VideoUrl = "https://example.com/video.mp4"
		return info, nil
	}, nil)
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse?url=https://v.douyin.com/test/", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("v1 URL 解析成功应返回 200，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "success" {
		t.Errorf("status 应为 success，实际: %v", resp["status"])
	}
	data := resp["data"].(map[string]any)
	if data["title"] != "测试视频" {
		t.Errorf("data.title 应为 '测试视频'，实际: %v", data["title"])
	}
	assertVideoParseInfoFields(t, data)
}

func TestIntegrationV1ParseIDSuccess(t *testing.T) {
	stubParserFuncs(t, nil, func(string, string) (*parser.VideoParseInfo, error) {
		info := &parser.VideoParseInfo{}
		info.Title = "ID解析视频"
		return info, nil
	})
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse/douyin/123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("v1 ID 解析成功应返回 200，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "success" {
		t.Errorf("status 应为 success，实际: %v", resp["status"])
	}
	data := resp["data"].(map[string]any)
	if data["title"] != "ID解析视频" {
		t.Errorf("data.title 应为 'ID解析视频'，实际: %v", data["title"])
	}
	assertVideoParseInfoFields(t, data)
}

func TestIntegrationV1ParseURL422OnParseFailure(t *testing.T) {
	stubParserFuncs(t, func(string) (*parser.VideoParseInfo, error) {
		return nil, fmt.Errorf("upstream parse failed")
	}, nil)
	r := setupTestRouter(t, false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse?url=https://v.douyin.com/test/", nil)
	r.ServeHTTP(w, req)

	if w.Code != 422 {
		t.Errorf("解析失败应返回 422，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "PARSE_FAILED" {
		t.Errorf("422 错误码应为 PARSE_FAILED，实际: %v", errObj["code"])
	}
}

func TestIntegrationV1ParseID422OnParseFailure(t *testing.T) {
	stubParserFuncs(t, nil, func(string, string) (*parser.VideoParseInfo, error) {
		return nil, fmt.Errorf("upstream parse failed")
	})
	r := setupTestRouter(t, false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse/douyin/123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 422 {
		t.Errorf("解析失败应返回 422，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "PARSE_FAILED" {
		t.Errorf("422 错误码应为 PARSE_FAILED，实际: %v", errObj["code"])
	}
}

func TestIntegrationLegacyRouteFormat(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/video/share/url/parse?url=invalid", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("旧路由应始终 200，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["code"]; !ok {
		t.Error("旧路由应包含 code 字段")
	}
	if _, ok := resp["msg"]; !ok {
		t.Error("旧路由应包含 msg 字段")
	}
}

func TestIntegrationCORSHeaders(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/v1/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != 204 {
		t.Errorf("OPTIONS 应返回 204，实际: %d", w.Code)
	}
}

func TestIntegrationNotFound(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/nonexistent", nil)
	r.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("不存在的路由应返回 404，实际: %d", w.Code)
	}
}

func TestIntegrationV1ParseIDMissingPathParam(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse/douyin", nil)
	r.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("缺少 video_id 的路由应返回 404，实际: %d", w.Code)
	}
}

func TestIntegrationLegacyIDParse(t *testing.T) {
	r := setupTestRouter(t, false)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/video/id/parse?source=unknown&video_id=123", nil)
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("旧路由应始终 200，实际: %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != float64(201) {
		t.Errorf("解析失败 code 应为 201，实际: %v", resp["code"])
	}
}

func TestIntegrationAuthProtectsV1Parse(t *testing.T) {
	r := setupTestRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/parse?url=test", nil)
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("无凭证访问 /api/v1/parse 应返回 401，实际: %d", w.Code)
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/v1/parse?url=test", nil)
	req2.SetBasicAuth("testuser", "testpass")
	r.ServeHTTP(w2, req2)
	if w2.Code == 401 {
		t.Error("正确凭证不应返回 401")
	}
}

func TestIntegrationAuthExemptPlatforms(t *testing.T) {
	r := setupTestRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/platforms", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/platforms 应无需认证返回 200，实际: %d", w.Code)
	}
}

func TestIntegrationAuthExemptHealth(t *testing.T) {
	r := setupTestRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/health 应无需认证返回 200，实际: %d", w.Code)
	}
}

func TestIntegrationAuthExemptWebUI(t *testing.T) {
	r := setupTestRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/ 应无需认证返回 200，实际: %d", w.Code)
	}
}

func TestIntegrationAuthProtectsLegacyRoutes(t *testing.T) {
	r := setupTestRouter(t, true)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/video/share/url/parse?url=test", nil)
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("无凭证访问旧路由应返回 401，实际: %d", w.Code)
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/video/id/parse?source=test&video_id=123", nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != 401 {
		t.Errorf("无凭证访问旧路由 /video/id/parse 应返回 401，实际: %d", w2.Code)
	}
}
