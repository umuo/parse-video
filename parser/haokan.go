package parser

import (
	"context"
	"errors"
	"net/url"

	"github.com/tidwall/gjson"
)

type haoKan struct {
}

func (h haoKan) parseShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	urlInfo, err := url.Parse(shareUrl)
	if err != nil {
		return nil, errors.New("parse share url fail")
	}
	if len(urlInfo.Query()["vid"]) <= 0 {
		return nil, errors.New("can not parse video id from share url")
	}
	return h.parseVideoID(ctx, urlInfo.Query()["vid"][0])
}

func (h haoKan) parseVideoID(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	reqUrl := "https://haokan.baidu.com/v?_format=json&vid=" + videoId
	client := newClient(SourceHaoKan)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	// 接口返回错误
	if gjson.GetBytes(res.Body(), "errno").Int() != 0 {
		return nil, errors.New(gjson.GetBytes(res.Body(), "error").String())
	}

	data := gjson.GetBytes(res.Body(), "data.apiData.curVideoMeta")
	title := data.Get("title").String()
	videoUrl := data.Get("playurl").String()
	cover := data.Get("poster").String()

	parseRes := &VideoParseInfo{
		Title:    title,
		VideoUrl: videoUrl,
		CoverUrl: cover,
	}
	parseRes.Author.Uid = data.Get("mth.mthid").String()
	parseRes.Author.Avatar = data.Get("mth.author_photo").String()
	parseRes.Author.Name = data.Get("mth.author_name").String()

	return parseRes, nil
}
