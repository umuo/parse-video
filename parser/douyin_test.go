package parser

import (
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
	ttwid, err := d.getTTWid()
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
	info, err := d.parseVideoID(videoId)
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
	info, err := d.parseShareUrl(shareUrl)
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
	shareUrl := "https://v.douyin.com/-k-MhQJJK6Y/"
	info, err := d.parseShareUrl(shareUrl)
	if err != nil {
		t.Logf("parseShareUrl(%s) note might be deleted/filtered by platform: %v", shareUrl, err)
		return
	}
	if info == nil {
		t.Fatalf("parseShareUrl(%s) returned nil", shareUrl)
	}
	if len(info.Images) == 0 {
		t.Errorf("expected non-empty Images, got 0")
	}
	t.Logf("Parsed note title: %s", info.Title)
	t.Logf("Parsed note author: %s", info.Author.Name)
	t.Logf("Parsed note images count: %d", len(info.Images))
	t.Logf("Parsed note music URL: %s", info.MusicUrl)
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


