package parser

import (
	"bytes"
	"context"
	"regexp"

	"github.com/PuerkitoBio/goquery"
)

type lvZhou struct {
}

func (l lvZhou) parseShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	client := newClient(SourceLvZhou)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(shareUrl)
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(res.Body()))
	if err != nil {
		return nil, err
	}
	videoUrl, _ := doc.Find("video").Attr("src")
	authorAvatar, _ := doc.Find("a.avatar img").Attr("src")
	videoCoverStyle, _ := doc.Find("div.video-cover").Attr("style")
	re := regexp.MustCompile("background-image:url\\((.*)\\)")
	var coverUrl string
	if findRes := re.FindSubmatch([]byte(videoCoverStyle)); len(findRes) >= 2 {
		coverUrl = string(findRes[1])
	}

	parseRes := &VideoParseInfo{
		Title:    doc.Find("div.status-title").Text(),
		VideoUrl: videoUrl,
		CoverUrl: coverUrl,
	}
	parseRes.Author.Name = doc.Find("div.nickname").Text()
	parseRes.Author.Avatar = authorAvatar

	return parseRes, nil
}

func (l lvZhou) parseVideoID(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	shareUrl := "https://m.oasis.weibo.cn/v1/h5/share?sid=" + videoId
	return l.parseShareUrl(ctx, shareUrl)
}
