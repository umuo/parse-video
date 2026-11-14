package parser

import (
	"context"
	"errors"

	"github.com/go-resty/resty/v2"
	"github.com/tidwall/gjson"
)

type huoShan struct {
}

func (h huoShan) parseVideoID(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	reqUrl := "https://share.huoshan.com/api/item/info?item_id=" + videoId
	client := newClient(SourceHuoShan)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	data := gjson.GetBytes(res.Body(), "data.item_info")
	videoUrl := data.Get("url").String()
	cover := data.Get("cover").String()

	parseRes := &VideoParseInfo{
		VideoUrl: videoUrl,
		CoverUrl: cover,
	}

	return parseRes, nil
}

func (h huoShan) parseShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	client := newClient(SourceHuoShan)
	// disable redirects in the HTTP client, get params before redirects
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(shareUrl)
	if err == nil {
		return nil, errShortLinkDidNotRedirect
	}
	if !errors.Is(err, resty.ErrAutoRedirectDisabled) {
		return nil, err
	}

	locationRes, err := checkedLocation(SourceHuoShan, res)
	if err != nil {
		return nil, err
	}

	videoId := locationRes.Query().Get("item_id")
	if len(videoId) <= 0 {
		return nil, errors.New("parse video id from share url fail")
	}

	return h.parseVideoID(ctx, videoId)
}
