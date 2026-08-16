package parser

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/tidwall/gjson"
	"golang.org/x/net/html"
)

var (
	douyinTTWid      string
	douyinTTWidMutex sync.RWMutex
)

type douYin struct{}

func (d douYin) getTTWid() (string, error) {
	douyinTTWidMutex.RLock()
	if douyinTTWid != "" {
		ttwid := douyinTTWid
		douyinTTWidMutex.RUnlock()
		return ttwid, nil
	}
	douyinTTWidMutex.RUnlock()

	douyinTTWidMutex.Lock()
	defer douyinTTWidMutex.Unlock()

	if douyinTTWid != "" {
		return douyinTTWid, nil
	}

	ttwid, err := d.fetchTTWid()
	if err != nil {
		return "", err
	}
	douyinTTWid = ttwid
	return ttwid, nil
}

func (d douYin) refreshTTWid() (string, error) {
	douyinTTWidMutex.Lock()
	defer douyinTTWidMutex.Unlock()

	ttwid, err := d.fetchTTWid()
	if err != nil {
		return "", err
	}
	douyinTTWid = ttwid
	return ttwid, nil
}

func (d douYin) fetchTTWid() (string, error) {
	client := newClient()
	client.SetRedirectPolicy(resty.NoRedirectPolicy())

	body := map[string]any{
		"region":        "cn",
		"aid":           1768,
		"needFid":       false,
		"service":       "www.ixigua.com",
		"migrate_info":  map[string]string{"ticket": "", "source": "node"},
		"cbUrlProtocol": "https",
		"union":         true,
	}

	res, err := client.R().
		SetHeader("Content-Type", "application/json").
		SetBody(body).
		Post("https://ttwid.bytedance.com/ttwid/union/register/")
	if err != nil {
		return "", fmt.Errorf("fetch douyin ttwid fail: %w", err)
	}

	for _, cookie := range res.Cookies() {
		if cookie.Name == "ttwid" && cookie.Value != "" {
			return cookie.Value, nil
		}
	}

	rawCookies := res.Header().Values("Set-Cookie")
	for _, c := range rawCookies {
		if strings.Contains(c, "ttwid=") {
			parts := strings.Split(c, ";")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "ttwid=") {
					return strings.TrimPrefix(part, "ttwid="), nil
				}
			}
		}
	}

	return "", errors.New("failed to acquire douyin ttwid cookie from register response")
}

func (d douYin) requestAwemeDetail(videoId string, ttwid string) ([]byte, error) {
	client := newClient()
	client.SetRedirectPolicy(resty.NoRedirectPolicy())

	reqUrl := fmt.Sprintf("https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=%s&aid=6383", videoId)
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader("Referer", "https://www.douyin.com/").
		SetHeader("Cookie", fmt.Sprintf("ttwid=%s", ttwid)).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	return res.Body(), nil
}

func (d douYin) parseVideoID(videoId string) (*VideoParseInfo, error) {
	ttwid, err := d.getTTWid()
	if err == nil && ttwid != "" {
		body, reqErr := d.requestAwemeDetail(videoId, ttwid)
		if reqErr == nil {
			data := gjson.GetBytes(body, "aweme_detail")
			if !data.Exists() || len(data.Map()) == 0 {
				// Refresh ttwid and try once more
				if refreshedTTWid, refErr := d.refreshTTWid(); refErr == nil {
					if refreshedBody, refErr2 := d.requestAwemeDetail(videoId, refreshedTTWid); refErr2 == nil {
						data = gjson.GetBytes(refreshedBody, "aweme_detail")
					}
				}
			}

			if data.Exists() && len(data.Map()) > 0 {
				return d.parseAwemeDetail(data)
			}
		}
	}

	// 降级回退到旧版 HTML 解析
	return d.parseVideoIDFromHTML(videoId)
}

func (d douYin) parseAwemeDetail(data gjson.Result) (*VideoParseInfo, error) {
	// 获取图集图片地址
	imageNodes := data.Get("images").Array()
	if len(imageNodes) == 0 {
		imageNodes = data.Get("image_post_info.images").Array()
	}

	images := make([]ImgInfo, 0, len(imageNodes))
	for _, imgItem := range imageNodes {
		urlList := imgItem.Get("display_image.url_list").Array()
		if len(urlList) == 0 {
			urlList = imgItem.Get("url_list").Array()
		}
		imageUrl := d.getNoWebpUrl(urlList)
		if len(imageUrl) > 0 {
			livePhotoUrl := imgItem.Get("video.play_addr.url_list.0").String()
			images = append(images, ImgInfo{
				Url:          imageUrl,
				LivePhotoUrl: livePhotoUrl,
			})
		}
	}

	var videoUrl string
	if len(images) == 0 {
		videoUri := data.Get("video.play_addr.uri").String()
		if videoUri != "" {
			videoUrl = fmt.Sprintf("https://www.iesdouyin.com/aweme/v1/play/?video_id=%s&ratio=1080p&line=0", videoUri)
		} else {
			videoUrl = data.Get("video.play_addr.url_list.0").String()
			if videoUrl == "" {
				videoUrl = data.Get("video.bit_rate.0.play_addr.url_list.0").String()
			}
			videoUrl = strings.ReplaceAll(videoUrl, "playwm", "play")
		}
	}

	// 获取音频地址
	musicUrl := data.Get("music.play_url.url_list.0").String()
	if musicUrl == "" {
		musicUrl = data.Get("video.play_addr.uri").String()
	}

	// 如果是图集，置空 videoUrl
	if len(images) > 0 {
		videoUrl = ""
	} else {
		musicUrl = ""
	}

	// 封面地址
	coverList := data.Get("video.cover.url_list").Array()
	if len(coverList) == 0 {
		coverList = data.Get("video.origin_cover.url_list").Array()
	}
	coverUrl := d.getNoWebpUrl(coverList)
	if coverUrl == "" && len(images) > 0 {
		coverUrl = images[0].Url
	}

	videoInfo := &VideoParseInfo{
		Title:    data.Get("desc").String(),
		VideoUrl: videoUrl,
		MusicUrl: musicUrl,
		CoverUrl: coverUrl,
		Images:   images,
	}

	videoInfo.Author.Uid = data.Get("author.sec_uid").String()
	if videoInfo.Author.Uid == "" {
		videoInfo.Author.Uid = data.Get("author.uid").String()
	}
	videoInfo.Author.Name = data.Get("author.nickname").String()
	videoInfo.Author.Avatar = data.Get("author.avatar_thumb.url_list.0").String()

	// 视频地址非空时，获取 302 重定向之后的视频地址
	if len(videoInfo.VideoUrl) > 0 {
		d.getRedirectUrl(videoInfo)
	}

	if videoInfo.VideoUrl == "" && len(videoInfo.Images) == 0 {
		return nil, errors.New("没有作品")
	}

	return videoInfo, nil
}

func (d douYin) parseVideoIDFromHTML(videoId string) (*VideoParseInfo, error) {
	reqUrl := fmt.Sprintf("https://www.iesdouyin.com/share/video/%s", videoId)

	client := newClient()
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1").
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	isNote := false
	resBody := res.Body()
	canonical, err := d.getCanonicalFromHTML(string(resBody))
	if err == nil && canonical != "" {
		if strings.Contains(canonical, "/note/") {
			isNote = true
		}
	}

	var jsonBytes []byte
	var data gjson.Result

	if isNote {
		webId := "75" + d.generateFixedLengthNumericID(15)
		aBogus := d.randSeq(64)

		reqUrl = fmt.Sprintf("https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/?reflow_source=reflow_page&web_id=%s&device_id=%s&aweme_ids=%%5B%s%%5D&request_source=200&a_bogus=%s", webId, webId, videoId, aBogus)
		res, err = client.R().
			SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1").
			Get(reqUrl)
		if err != nil {
			return nil, err
		}

		jsonBytes = res.Body()
		data = gjson.GetBytes(jsonBytes, "aweme_details.0")
		if !data.Exists() {
			isNote = false
		}
	}

	if !isNote {
		re := regexp.MustCompile(`window._ROUTER_DATA\s*=\s*(.*?)</script>`)
		findRes := re.FindSubmatch(resBody)
		if len(findRes) < 2 {
			return nil, errors.New("parse video json info from html fail")
		}

		jsonBytes = bytes.TrimSpace(findRes[1])
		data = gjson.GetBytes(jsonBytes, "loaderData.video_(id)/page.videoInfoRes.item_list.0")
	}

	if !data.Exists() {
		filterObj := gjson.GetBytes(
			jsonBytes,
			fmt.Sprintf(`loaderData.video_(id)/page.videoInfoRes.filter_list.#(aweme_id=="%s")`, videoId),
		)

		return nil, fmt.Errorf(
			"get video info fail: %s - %s",
			filterObj.Get("filter_reason"),
			filterObj.Get("detail_msg"),
		)
	}

	// 获取图集图片地址
	imagesObjArr := data.Get("images").Array()
	images := make([]ImgInfo, 0, len(imagesObjArr))
	for _, imageItem := range imagesObjArr {
		urlList := imageItem.Get("url_list").Array()
		imageUrl := d.getNoWebpUrl(urlList)
		if len(imageUrl) > 0 {
			images = append(images, ImgInfo{
				Url:          imageUrl,
				LivePhotoUrl: imageItem.Get("video.play_addr.url_list.0").String(),
			})
		}
	}

	var videoUrl string
	if !isNote {
		videoUrl = data.Get("video.play_addr.url_list.0").String()
		videoUrl = strings.ReplaceAll(videoUrl, "playwm", "play")
	}

	musicUrl := data.Get("video.play_addr.uri").String()
	if len(images) > 0 {
		videoUrl = ""
	} else {
		musicUrl = ""
	}

	urlList := data.Get("video.cover.url_list").Array()
	coverUrl := d.getNoWebpUrl(urlList)

	videoInfo := &VideoParseInfo{
		Title:    data.Get("desc").String(),
		VideoUrl: videoUrl,
		MusicUrl: musicUrl,
		CoverUrl: coverUrl,
		Images:   images,
	}
	videoInfo.Author.Uid = data.Get("author.sec_uid").String()
	videoInfo.Author.Name = data.Get("author.nickname").String()
	videoInfo.Author.Avatar = data.Get("author.avatar_thumb.url_list.0").String()

	if len(videoInfo.VideoUrl) > 0 {
		d.getRedirectUrl(videoInfo)
	}

	if videoInfo.VideoUrl == "" && len(videoInfo.Images) == 0 {
		return nil, errors.New("没有作品")
	}

	return videoInfo, nil
}

func (d douYin) parseShareUrl(shareUrl string) (*VideoParseInfo, error) {
	urlRes, err := url.Parse(shareUrl)
	if err != nil {
		return nil, err
	}

	switch urlRes.Host {
	case "www.iesdouyin.com", "www.douyin.com", "iesdouyin.com", "douyin.com":
		return d.parsePcShareUrl(shareUrl)
	case "v.douyin.com":
		return d.parseAppShareUrl(shareUrl)
	}

	return nil, fmt.Errorf("douyin not support this host: %s", urlRes.Host)
}

func (d douYin) parseAppShareUrl(shareUrl string) (*VideoParseInfo, error) {
	client := newClient()
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	res, err := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		Get(shareUrl)
	if !errors.Is(err, resty.ErrAutoRedirectDisabled) {
		return nil, err
	}

	locationRes, err := res.RawResponse.Location()
	if err != nil {
		return nil, err
	}

	videoId, err := d.parseVideoIdFromPath(locationRes.Path)
	if err != nil {
		// 尝试从整个 URL（含 Query）提取
		videoId, err = d.parseVideoIdFromPath(locationRes.String())
		if err != nil {
			return nil, err
		}
	}
	if len(videoId) <= 0 {
		return nil, errors.New("parse video id from share url fail")
	}

	// 西瓜视频解析方式不一样
	if strings.Contains(locationRes.Host, "ixigua.com") {
		return xiGua{}.parseVideoID(videoId)
	}

	return d.parseVideoID(videoId)
}

func (d douYin) parsePcShareUrl(shareUrl string) (*VideoParseInfo, error) {
	videoId, err := d.parseVideoIdFromPath(shareUrl)
	if err != nil {
		return nil, err
	}
	return d.parseVideoID(videoId)
}

func (d douYin) parseVideoIdFromPath(urlPath string) (string, error) {
	if len(urlPath) <= 0 {
		return "", errors.New("url path is empty")
	}

	urlPathParse, err := url.Parse(urlPath)
	if err != nil {
		return "", err
	}

	// 判断 modal_id 参数（如精选、发现等页面）
	videoId := urlPathParse.Query().Get("modal_id")
	if len(videoId) > 0 {
		return videoId, nil
	}

	// 判断其他页面的视频/笔记
	urlPath = strings.Trim(urlPathParse.Path, "/")
	urlSplit := strings.Split(urlPath, "/")

	if len(urlSplit) > 0 {
		lastPart := urlSplit[len(urlSplit)-1]
		if len(lastPart) > 0 {
			return lastPart, nil
		}
	}

	return "", errors.New("parse video id from path fail")
}

func (d douYin) getRedirectUrl(videoInfo *VideoParseInfo) {
	if videoInfo.VideoUrl == "" {
		return
	}
	// 如果已经是重定向 API 或 CDN 直链，无需再跟踪重定向
	if strings.Contains(videoInfo.VideoUrl, "/aweme/v1/play/") ||
		strings.Contains(videoInfo.VideoUrl, "zjcdn.com") ||
		strings.Contains(videoInfo.VideoUrl, "douyinvod.com") ||
		strings.Contains(videoInfo.VideoUrl, "bytevcloud.com") ||
		strings.Contains(videoInfo.VideoUrl, "tos-cn-") {
		return
	}

	client := newClient()
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	client.SetDoNotParseResponse(true)
	res2, _ := client.R().
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		SetHeader("Range", "bytes=0-0").
		Get(videoInfo.VideoUrl)
	if res2 != nil && res2.RawResponse != nil {
		defer res2.RawResponse.Body.Close()
		locationRes, _ := res2.RawResponse.Location()
		if locationRes != nil {
			(*videoInfo).VideoUrl = locationRes.String()
		}
	}
}

func (d douYin) randSeq(n int) string {
	letters := []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (d douYin) generateFixedLengthNumericID(length int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	max2 := int64(1)
	for i := 0; i < length; i++ {
		max2 *= 10
	}

	randomNum := r.Int63n(max2)
	return fmt.Sprintf("%0*d", length, randomNum)
}

func (d douYin) getNoWebpUrl(urlList []gjson.Result) string {
	var imageUrl string
	found := false
	for _, urllink := range urlList {
		urlStr := urllink.String()
		if !strings.Contains(urlStr, ".webp") {
			imageUrl = urlStr
			found = true
			break
		}
	}

	if !found && len(urlList) > 0 {
		imageUrl = urlList[0].String()
	}

	return imageUrl
}

func (d douYin) getCanonicalFromHTML(htmlContent string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}

	return d.findCanonical(doc), nil
}

func (d douYin) findCanonical(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "link" {
		var rel, href string
		for _, attr := range n.Attr {
			switch attr.Key {
			case "rel":
				rel = attr.Val
			case "href":
				href = attr.Val
			}
		}
		if rel == "canonical" && href != "" {
			return href
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if result := d.findCanonical(c); result != "" {
			return result
		}
	}

	return ""
}
