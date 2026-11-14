package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/tidwall/gjson"
)

func Test_douYin_parseIdFromPath(t *testing.T) {
	type args struct {
		path string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{"抖音视频", args{"/share/video/7329354490828623130/"}, "7329354490828623130", false},
		{"抖音视频短链重定向", args{"https://www.iesdouyin.com/share/video/7674213987094344037/?region=CN&mid=7674214312010500899"}, "7674213987094344037", false},
		{"抖音PC端视频", args{"https://www.douyin.com/video/7674213987094344037"}, "7674213987094344037", false},
		{"抖音精选modal_id", args{"https://www.douyin.com/jingxuan?modal_id=7555093909760789812"}, "7555093909760789812", false},
		{"抖音图文笔记", args{"https://www.douyin.com/note/7424432820954598707"}, "7424432820954598707", false},
		{"西瓜视频", args{"/douyin/share/video/7144194760184594977"}, "7144194760184594977", false},
		{"异常视频", args{""}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := douYin{}
			got, err := d.parseVideoIdFromPath(tt.args.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVideoIdFromPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseVideoIdFromPath() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_douYin_fetchTTWid(t *testing.T) {
	d := douYin{}
	ttwid, err := d.getTTWid(context.Background())
	if err != nil {
		t.Fatalf("getTTWid() failed: %v", err)
	}
	if len(ttwid) == 0 {
		t.Fatal("getTTWid() returned empty ttwid")
	}
	t.Logf("acquired ttwid: %s", ttwid)
}

func Test_douYin_parseVideoID_Success(t *testing.T) {
	d := douYin{}
	videoId := "7677912809321008418"
	info, err := d.parseVideoID(context.Background(), videoId)
	if err != nil {
		t.Fatalf("parseVideoID(%s) failed: %v", videoId, err)
	}
	if info == nil {
		t.Fatalf("parseVideoID(%s) returned nil", videoId)
	}
	if info.Title == "" {
		t.Errorf("expected non-empty Title, got empty")
	}
	if info.Author.Name == "" {
		t.Errorf("expected non-empty Author.Name, got empty")
	}
	if info.VideoUrl == "" && len(info.Images) == 0 {
		t.Errorf("expected non-empty VideoUrl or Images")
	}
	t.Logf("Parsed video title: %s", info.Title)
	t.Logf("Parsed video author: %s", info.Author.Name)
	t.Logf("Parsed video URL: %s", info.VideoUrl)
	t.Logf("Parsed short URL: %s", info.ShortUrl)
	t.Logf("Parsed cover URL: %s", info.CoverUrl)
}

func Test_douYin_parseShareUrl_Success(t *testing.T) {
	d := douYin{}
	shareUrl := "https://v.douyin.com/loqfe_AciR0/"
	info, err := d.parseShareUrl(context.Background(), shareUrl)
	if err != nil {
		t.Fatalf("parseShareUrl(%s) failed: %v", shareUrl, err)
	}
	if info == nil {
		t.Fatalf("parseShareUrl(%s) returned nil", shareUrl)
	}
	if info.Title == "" {
		t.Errorf("expected non-empty Title, got empty")
	}
	t.Logf("Parsed share URL title: %s", info.Title)
	t.Logf("Parsed share URL author: %s", info.Author.Name)
	t.Logf("Parsed share URL video: %s", info.VideoUrl)
	t.Logf("Parsed share URL short video: %s", info.ShortUrl)
}

func Test_douYin_parseNoteShareUrl_Success(t *testing.T) {
	d := douYin{}

	testCases := []struct {
		name     string
		shareUrl string
		minImgs  int
	}{
		{
			name:     "壁纸图文作品",
			shareUrl: "https://v.douyin.com/Cn-ZILqSTsA/",
			minImgs:  12,
		},
		{
			name:     "iOS壁纸图文作品",
			shareUrl: "https://v.douyin.com/-k-MhQJJK6Y/",
			minImgs:  1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := d.parseShareUrl(context.Background(), tc.shareUrl)
			if err != nil {
				t.Fatalf("parseShareUrl(%s) failed: %v", tc.shareUrl, err)
			}
			if info == nil {
				t.Fatalf("parseShareUrl(%s) returned nil", tc.shareUrl)
			}
			if len(info.Images) < tc.minImgs {
				t.Errorf("expected at least %d images, got %d", tc.minImgs, len(info.Images))
			}
			if info.Title == "" {
				t.Errorf("expected non-empty title")
			}
			if info.Author.Name == "" {
				t.Errorf("expected non-empty author name")
			}
			t.Logf("[%s] title=%s, author=%s, images=%d", tc.name, info.Title, info.Author.Name, len(info.Images))
		})
	}
}

func Test_douYin_parseNoteVideoID_Success(t *testing.T) {
	d := douYin{}
	videoId := "7109738393529109771"
	info, err := d.parseVideoID(context.Background(), videoId)
	if err != nil {
		t.Fatalf("parseVideoID(%s) failed: %v", videoId, err)
	}
	if info == nil {
		t.Fatalf("parseVideoID(%s) returned nil", videoId)
	}
	if len(info.Images) != 12 {
		t.Errorf("expected 12 images, got %d", len(info.Images))
	}
	if info.Title != "全面屏2k屏高清壁纸" {
		t.Errorf("expected title '全面屏2k屏高清壁纸', got %s", info.Title)
	}
	if info.Author.Name != "高清壁纸" {
		t.Errorf("expected author '高清壁纸', got %s", info.Author.Name)
	}
}

func Test_douYin_pickBestVideoUrl_AvoidV26(t *testing.T) {
	d := douYin{}

	tests := []struct {
		name     string
		jsonRaw  string
		expected string
	}{
		{
			name:     "优先避开 v26-web 选取 v11-default",
			jsonRaw:  `["https://v26-web.douyinvod.com/abc/video.mp4", "https://v11-default.365yg.com/def/video.mp4", "https://api-play.amemv.com/play"]`,
			expected: "https://v11-default.365yg.com/def/video.mp4",
		},
		{
			name:     "优先避开 v26- 选取 v5- 节点",
			jsonRaw:  `["https://v26-default.365yg.com/abc/video.mp4", "https://v5-se-ex-mc-default.365yg.com/def/video.mp4"]`,
			expected: "https://v5-se-ex-mc-default.365yg.com/def/video.mp4",
		},
		{
			name:     "全为 v26 时平稳保底",
			jsonRaw:  `["https://v26-web.douyinvod.com/abc/video.mp4"]`,
			expected: "https://v26-web.douyinvod.com/abc/video.mp4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := gjson.Parse(tt.jsonRaw).Array()
			got := d.pickBestVideoUrl(list)
			if got != tt.expected {
				t.Errorf("pickBestVideoUrl() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func Test_douYin_MergedFlow_WithMockProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			http.Error(w, "no tls in mock proxy", http.StatusBadGateway)
			return
		}
		switch {
		case r.URL.Host == "www.iesdouyin.com" && r.URL.Path == "/aweme/v1/play/":
			http.Redirect(w, r, "http://v99.douyinvod.com/resolved.mp4", http.StatusFound)
		case r.URL.Host == "api-play.amemv.com" && r.URL.Path == "/livephoto":
			http.Redirect(w, r, "http://v3.zjcdn.com/live.mp4", http.StatusFound)
		case r.URL.Host == "www.iesdouyin.com" && r.URL.Path == "/evil":
			http.Redirect(w, r, "http://evil.example.net/steal", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(resetProxy)
	if err := InitProxy(proxy.URL); err != nil {
		t.Fatalf("InitProxy() error = %v", err)
	}

	d := douYin{}
	ctx := context.Background()

	t.Run("outbound allowlist and checked redirect", func(t *testing.T) {
		allowedTargets := []string{
			"https://ttwid.bytedance.com/ttwid/union/register/",
			"https://v11-default.365yg.com/def.mp4",
			"https://v3.bytevcloud.com/a.mp4",
			"https://douyin.com/video/123",
			"https://iesdouyin.com/share/video/123",
		}
		for _, raw := range allowedTargets {
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", raw, err)
			}
			if !outboundURLAllowed(SourceDouYin, parsed) {
				t.Errorf("outboundURLAllowed(SourceDouYin, %q) = false, want true", raw)
			}
		}

		got := d.resolveRedirect(ctx, "http://www.iesdouyin.com/aweme/v1/play/?video_id=vid123")
		if got != "http://v99.douyinvod.com/resolved.mp4" {
			t.Errorf("resolveRedirect(allowed) = %q, want %q", got, "http://v99.douyinvod.com/resolved.mp4")
		}

		evilTarget := "http://www.iesdouyin.com/evil"
		if got := d.resolveRedirect(ctx, evilTarget); got != evilTarget {
			t.Errorf("resolveRedirect(evil) = %q, want fallback %q", got, evilTarget)
		}
	})

	t.Run("parseAwemeDetail video and livephoto", func(t *testing.T) {
		videoDetail := gjson.Parse(`{
			"desc": "slides title",
			"author": {"sec_uid": "u1", "nickname": "author1"},
			"video": {
				"cover": {"url_list": ["http://p3.douyinpic.com/cover.jpeg"]},
				"play_addr": {
					"uri": "vid123",
					"url_list": [
						"http://v26-web.douyinvod.com/bad.mp4",
						"http://v11-default.365yg.com/good.mp4"
					]
				}
			}
		}`)
		info, err := d.parseAwemeDetail(ctx, videoDetail)
		if err != nil {
			t.Fatalf("parseAwemeDetail(video) error = %v", err)
		}
		if info.VideoUrl != "http://v11-default.365yg.com/good.mp4" {
			t.Errorf("VideoUrl = %q, want %q", info.VideoUrl, "http://v11-default.365yg.com/good.mp4")
		}
		wantShort := "https://www.iesdouyin.com/aweme/v1/play/?video_id=vid123&ratio=1080p&line=0"
		if info.ShortUrl != wantShort {
			t.Errorf("ShortUrl = %q, want %q", info.ShortUrl, wantShort)
		}
		if info.CoverUrl != "http://p3.douyinpic.com/cover.jpeg" {
			t.Errorf("CoverUrl = %q, want %q", info.CoverUrl, "http://p3.douyinpic.com/cover.jpeg")
		}

		noteDetail := gjson.Parse(`{
			"desc": "note title",
			"author": {"sec_uid": "u2", "nickname": "author2"},
			"cover": {"url_list": ["http://p3.douyinpic.com/1.jpeg"]},
			"images": [
				{
					"url_list": ["http://p3.douyinpic.com/1.webp", "http://p3.douyinpic.com/1.jpeg"],
					"video": {"play_addr": {"url_list": ["http://api-play.amemv.com/livephoto"]}}
				}
			]
		}`)
		noteInfo, err := d.parseAwemeDetail(ctx, noteDetail)
		if err != nil {
			t.Fatalf("parseAwemeDetail(note) error = %v", err)
		}
		if len(noteInfo.Images) != 1 {
			t.Fatalf("len(Images) = %d, want 1", len(noteInfo.Images))
		}
		if noteInfo.Images[0].Url != "http://p3.douyinpic.com/1.jpeg" {
			t.Errorf("Images[0].Url = %q, want %q", noteInfo.Images[0].Url, "http://p3.douyinpic.com/1.jpeg")
		}
		if noteInfo.Images[0].LivePhotoUrl != "http://v3.zjcdn.com/live.mp4" {
			t.Errorf("Images[0].LivePhotoUrl = %q, want %q", noteInfo.Images[0].LivePhotoUrl, "http://v3.zjcdn.com/live.mp4")
		}
		if noteInfo.VideoUrl != "" {
			t.Errorf("note VideoUrl = %q, want empty", noteInfo.VideoUrl)
		}
		if noteInfo.ShortUrl != "" {
			t.Errorf("note ShortUrl = %q, want empty", noteInfo.ShortUrl)
		}
	})

	t.Run("parseSEOHTML fallback", func(t *testing.T) {
		seoHTML := []byte(`<!doctype html><html><head><script type="application/ld+json">{"@context":"https://schema.org","headline":"壁纸合集","image":["https://p3.douyinpic.com/1.jpeg","https://p3.douyinpic.com/2.jpeg"],"author":{"name":"作者A","url":"https://www.douyin.com/user/MS4wLjAB"}}</script></head></html>`)
		info, err := d.parseSEOHTML(seoHTML)
		if err != nil {
			t.Fatalf("parseSEOHTML() error = %v", err)
		}
		if info.Title != "壁纸合集" {
			t.Errorf("Title = %q, want %q", info.Title, "壁纸合集")
		}
		if len(info.Images) != 2 {
			t.Errorf("len(Images) = %d, want 2", len(info.Images))
		}
		if info.Author.Name != "作者A" || info.Author.Uid != "MS4wLjAB" {
			t.Errorf("Author = %+v, want {Uid:MS4wLjAB Name:作者A}", info.Author)
		}
	})
}
