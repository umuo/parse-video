package parser

import (
	"bytes"
	"context"
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

func (d douYin) getTTWid(ctx context.Context) (string, error) {
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

	ttwid, err := d.fetchTTWid(ctx)
	if err != nil {
		return "", err
	}
	douyinTTWid = ttwid
	return ttwid, nil
}

func (d douYin) refreshTTWid(ctx context.Context) (string, error) {
	douyinTTWidMutex.Lock()
	defer douyinTTWidMutex.Unlock()

	ttwid, err := d.fetchTTWid(ctx)
	if err != nil {
		return "", err
	}
	douyinTTWid = ttwid
	return ttwid, nil
}

func (d douYin) fetchTTWid(ctx context.Context) (string, error) {
	client := newClient(SourceDouYin)
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

	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderContentType, "application/json").
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

func (d douYin) requestAwemeDetail(ctx context.Context, videoId string, ttwid string) ([]byte, error) {
	client := newClient(SourceDouYin)
	client.SetRedirectPolicy(resty.NoRedirectPolicy())

	reqUrl := fmt.Sprintf("https://www.douyin.com/aweme/v1/web/aweme/detail/?aweme_id=%s&aid=6383", videoId)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader(HttpHeaderReferer, "https://www.douyin.com/").
		SetHeader(HttpHeaderCookie, fmt.Sprintf("ttwid=%s", ttwid)).
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	return res.Body(), nil
}

func (d douYin) parseVideoID(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	// 1. 优先尝试移动端 slidesinfo 接口（同时支持视频与图文，且免 ArgusSecurity 插件拦截）
	if info, err := d.parseVideoIDFromSlides(ctx, videoId); err == nil && info != nil {
		return info, nil
	}

	// 2a. 若部署环境变量提供 PARSE_VIDEO_DOUYIN_COOKIE，优先使用带 a_bogus 签名的官方详情接口
	client := newClient(SourceDouYin)
	if data, err := d.fetchNativeVideoDetail(ctx, client, videoId); err == nil && data.Exists() && len(data.Map()) > 0 {
		if info, parseErr := d.parseAwemeDetail(ctx, data); parseErr == nil && info != nil {
			return info, nil
		}
	}

	// 2b. 尝试使用匿名 ttwid 请求网页详情接口
	if ttwid, err := d.getTTWid(ctx); err == nil && ttwid != "" {
		var data gjson.Result
		if body, reqErr := d.requestAwemeDetail(ctx, videoId, ttwid); reqErr == nil {
			data = gjson.GetBytes(body, "aweme_detail")
		}
		if !data.Exists() || len(data.Map()) == 0 {
			// Refresh ttwid and try once more
			if refreshedTTWid, refErr := d.refreshTTWid(ctx); refErr == nil && refreshedTTWid != "" {
				if refreshedBody, refErr2 := d.requestAwemeDetail(ctx, videoId, refreshedTTWid); refErr2 == nil {
					data = gjson.GetBytes(refreshedBody, "aweme_detail")
				}
				if !data.Exists() || len(data.Map()) == 0 {
					if signedData, signErr := newDouyinWebDetailFetcher(client, "ttwid="+refreshedTTWid).fetch(ctx, videoId); signErr == nil {
						data = signedData
					}
				}
			}
		}

		if data.Exists() && len(data.Map()) > 0 {
			if info, parseErr := d.parseAwemeDetail(ctx, data); parseErr == nil && info != nil {
				return info, nil
			}
		}
	}

	// 3. 尝试搜索引擎 SEO 结构化数据解析（完美支持被 slidesinfo filter_list 过滤的图文笔记）
	if info, err := d.parseVideoIDFromSEO(ctx, videoId); err == nil && info != nil {
		return info, nil
	}

	// 4. 降级回退到旧版 HTML 解析
	return d.parseVideoIDFromHTML(ctx, videoId)
}

func (d douYin) parseVideoIDFromSlides(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	reqUrl := fmt.Sprintf("https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/?aweme_ids=%%5B%s%%5D", videoId)

	client := newClient(SourceDouYin)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1").
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	data := gjson.GetBytes(res.Body(), "aweme_details.0")
	if !data.Exists() || len(data.Map()) == 0 {
		return nil, errors.New("slidesinfo: aweme_details empty")
	}

	return d.parseAwemeDetail(ctx, data)
}

func (d douYin) parseAwemeDetail(ctx context.Context, data gjson.Result) (*VideoParseInfo, error) {
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
			livePhotoUrl := d.pickBestVideoUrl(imgItem.Get("video.play_addr.url_list").Array())
			if livePhotoUrl != "" {
				if resolved := d.resolveRedirect(ctx, livePhotoUrl); resolved != "" {
					livePhotoUrl = resolved
				}
			}
			images = append(images, ImgInfo{
				Url:          imageUrl,
				LivePhotoUrl: livePhotoUrl,
			})
		}
	}

	var videoUrl string
	var shortUrl string
	if len(images) == 0 {
		videoUri := data.Get("video.play_addr.uri").String()
		if videoUri != "" {
			shortUrl = fmt.Sprintf("https://www.iesdouyin.com/aweme/v1/play/?video_id=%s&ratio=1080p&line=0", videoUri)
		}

		// 优选不带 -web 的 CDN 直链（避免网页端防盗链 403）
		videoUrl = d.pickBestVideoUrl(data.Get("video.play_addr.url_list").Array())
		if videoUrl == "" {
			videoUrl = d.pickBestVideoUrl(data.Get("video.bit_rate.0.play_addr.url_list").Array())
		}
		if videoUrl == "" && shortUrl != "" {
			videoUrl = d.resolveRedirect(ctx, shortUrl)
		}
		if videoUrl == "" {
			videoUrl = data.Get("video.play_addr.url_list.0").String()
		}
		videoUrl = strings.ReplaceAll(videoUrl, "playwm", "play")
	}

	// 获取音频地址
	musicUrl := data.Get("music.play_url.url_list.0").String()
	if musicUrl == "" {
		musicUrl = data.Get("video.play_addr.uri").String()
	}

	// 如果是图集，置空 videoUrl
	if len(images) > 0 {
		videoUrl = ""
		shortUrl = ""
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
		ShortUrl: shortUrl,
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
		d.getRedirectUrl(ctx, videoInfo)
	}

	if videoInfo.VideoUrl == "" && len(videoInfo.Images) == 0 {
		return nil, errors.New("没有作品")
	}

	return videoInfo, nil
}

func (d douYin) parseVideoIDFromHTML(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	reqUrl := fmt.Sprintf("https://www.iesdouyin.com/share/video/%s", videoId)

	client := newClient(SourceDouYin)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1").
		Get(reqUrl)
	if err != nil {
		return nil, err
	}

	isNote := false
	resBody := res.Body()
	canonical, err := d.getCanonicalFromHTML(string(resBody))
	if err == nil && canonical != "" {
		// 判断字符串中是否有 /note/ 字符
		if strings.Contains(canonical, "/note/") {
			isNote = true
		}
	}

	var jsonBytes []byte
	var data gjson.Result

	// 获取图集
	if isNote {
		webId := "75" + d.generateFixedLengthNumericID(15)
		aBogus := d.randSeq(64)

		reqUrl = fmt.Sprintf("https://www.iesdouyin.com/web/api/v2/aweme/slidesinfo/?reflow_source=reflow_page&web_id=%s&device_id=%s&aweme_ids=%%5B%s%%5D&request_source=200&a_bogus=%s", webId, webId, videoId, aBogus)
		res, err = client.R().SetContext(ctx).
			SetHeader(HttpHeaderUserAgent, "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1").
			Get(reqUrl)
		if err != nil {
			return nil, err
		}

		jsonBytes = res.Body()
		data = gjson.GetBytes(jsonBytes, "aweme_details.0")
		if !data.Exists() {
			// 设置为 false，好让下面判断
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
		if filterObj.Exists() {
			return nil, fmt.Errorf(
				"get video info fail: %s - %s",
				filterObj.Get("filter_reason"),
				filterObj.Get("detail_msg"),
			)
		}

		return nil, errors.New("parse video json info from html fail")
	}

	// 获取图集图片地址
	imagesObjArr := data.Get("images").Array()
	images := make([]ImgInfo, 0, len(imagesObjArr))
	for _, imageItem := range imagesObjArr {
		urlList := imageItem.Get("url_list").Array()
		// 优先获取非 .webp 格式的图片 url
		imageUrl := d.getNoWebpUrl(urlList)
		if len(imageUrl) > 0 {
			livePhotoUrl := d.pickBestVideoUrl(imageItem.Get("video.play_addr.url_list").Array())
			if livePhotoUrl != "" {
				if resolved := d.resolveRedirect(ctx, livePhotoUrl); resolved != "" {
					livePhotoUrl = resolved
				}
			}
			images = append(images, ImgInfo{
				Url:          imageUrl,
				LivePhotoUrl: livePhotoUrl,
			})
		}
	}

	var videoUrl string
	var shortUrl string
	if !isNote {
		// 获取视频播放地址
		videoUrl = data.Get("video.play_addr.url_list.0").String()
		videoUrl = strings.ReplaceAll(videoUrl, "playwm", "play")

		videoUri := data.Get("video.play_addr.uri").String()
		if videoUri != "" {
			shortUrl = fmt.Sprintf("https://www.iesdouyin.com/aweme/v1/play/?video_id=%s&ratio=1080p&line=0", videoUri)
		}
	}

	// 获取音频地址（图集时，video.play_addr.uri 是音频地址；视频时不是音频）
	musicUrl := data.Get("video.play_addr.uri").String()

	// 如果图集地址不为空时，因为没有视频，上面抖音返回的视频地址无法访问，置空处理
	// 图集时，musicUrl 是音频地址；视频时，musicUrl 不是音频，置空
	if len(images) > 0 {
		videoUrl = ""
		shortUrl = ""
	} else {
		musicUrl = ""
	}

	urlList := data.Get("video.cover.url_list").Array()
	// 优先获取非 .webp 格式的图片 url
	coverUrl := d.getNoWebpUrl(urlList)

	videoInfo := &VideoParseInfo{
		Title:    data.Get("desc").String(),
		VideoUrl: videoUrl,
		ShortUrl: shortUrl,
		MusicUrl: musicUrl,
		CoverUrl: coverUrl,
		Images:   images,
	}
	videoInfo.Author.Uid = data.Get("author.sec_uid").String()
	videoInfo.Author.Name = data.Get("author.nickname").String()
	videoInfo.Author.Avatar = data.Get("author.avatar_thumb.url_list.0").String()

	// 视频地址非空时，获取302重定向之后的视频地址
	// 图集时，视频地址为空，不处理
	if len(videoInfo.VideoUrl) > 0 {
		d.getRedirectUrl(ctx, videoInfo)
	}

	if videoInfo.VideoUrl == "" && len(videoInfo.Images) == 0 {
		return nil, errors.New("没有作品")
	}

	return videoInfo, nil
}

func (d douYin) parseShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	urlRes, err := url.Parse(shareUrl)
	if err != nil {
		return nil, err
	}

	switch httpHostname(urlRes) {
	case "www.iesdouyin.com", "www.douyin.com", "iesdouyin.com", "douyin.com":
		return d.parsePcShareUrl(ctx, shareUrl) // 解析电脑网页端链接
	case "v.douyin.com":
		return d.parseAppShareUrl(ctx, shareUrl) // 解析App分享链接
	}

	return nil, fmt.Errorf("douyin not support this host: %s", urlRes.Host)
}

func (d douYin) parseAppShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	// 适配App分享链接类型:
	// https://v.douyin.com/xxxxxx/

	client := newClient(SourceDouYin)
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

	locationRes, err := checkedLocation(SourceDouYin, res)
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
	if hostMatches(httpHostname(locationRes), "ixigua.com") {
		return xiGua{}.parseVideoID(ctx, videoId)
	}

	return d.parseVideoID(ctx, videoId)
}

func (d douYin) parsePcShareUrl(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	// 适配电脑网页端链接类型
	// https://www.iesdouyin.com/share/video/xxxxxx/
	// https://www.douyin.com/video/xxxxxx
	videoId, err := d.parseVideoIdFromPath(shareUrl)
	if err != nil {
		return nil, err
	}
	return d.parseVideoID(ctx, videoId)
}

func (d douYin) parseVideoIdFromPath(urlPath string) (string, error) {
	if len(urlPath) <= 0 {
		return "", errors.New("url path is empty")
	}

	urlPathParse, err := url.Parse(urlPath)
	if err != nil {
		return "", err
	}

	// 判断网页精选页面的视频
	// https://www.douyin.com/jingxuan?modal_id=7555093909760789812
	videoId := urlPathParse.Query().Get("modal_id")
	if len(videoId) > 0 {
		return videoId, nil
	}

	// 判断其他页面的视频/笔记
	// https://www.iesdouyin.com/share/video/7424432820954598707/?region=CN&mid=7424432976273869622&u_code=0
	urlPath = strings.Trim(urlPathParse.Path, "/")
	urlSplit := strings.Split(urlPath, "/")

	// 获取最后一个元素
	if len(urlSplit) > 0 {
		lastPart := urlSplit[len(urlSplit)-1]
		if len(lastPart) > 0 {
			return lastPart, nil
		}
	}

	return "", errors.New("parse video id from path fail")
}

func (d douYin) isHotlinkProtected(urlStr string) bool {
	return strings.Contains(urlStr, "-web") ||
		strings.Contains(urlStr, "://v26-") ||
		strings.Contains(urlStr, "/aweme/v1/play/")
}

func (d douYin) pickBestVideoUrl(urlList []gjson.Result) string {
	// 第一优先级：不带防盗链限制（非 -web, 非 v26-）的高质量真实 CDN 直链（zjcdn, douyinvod, bytevcloud, tos-cn, 365yg）
	for _, u := range urlList {
		urlStr := u.String()
		if urlStr == "" || d.isHotlinkProtected(urlStr) {
			continue
		}
		if strings.Contains(urlStr, "zjcdn.com") || strings.Contains(urlStr, "douyinvod.com") || strings.Contains(urlStr, "bytevcloud.com") || strings.Contains(urlStr, "tos-cn-") || strings.Contains(urlStr, "365yg.com") {
			return urlStr
		}
	}
	// 第二优先级：不带防盗链限制的任意有效非 API 链接
	for _, u := range urlList {
		urlStr := u.String()
		if urlStr != "" && !d.isHotlinkProtected(urlStr) {
			return urlStr
		}
	}
	// 第三优先级：只要不带 -web 且不带 v26- 的任意链接
	for _, u := range urlList {
		urlStr := u.String()
		if urlStr != "" && !strings.Contains(urlStr, "-web") && !strings.Contains(urlStr, "://v26-") {
			return urlStr
		}
	}
	// 第四优先级：不带 -web 的链接
	for _, u := range urlList {
		urlStr := u.String()
		if urlStr != "" && !strings.Contains(urlStr, "-web") {
			return urlStr
		}
	}
	if len(urlList) > 0 {
		return urlList[0].String()
	}
	return ""
}

func (d douYin) resolveRedirect(ctx context.Context, targetUrl string) string {
	client := newClient(SourceDouYin)
	client.SetRedirectPolicy(resty.NoRedirectPolicy())
	client.SetDoNotParseResponse(true)
	res, err := client.R().SetContext(ctx).
		SetHeader(HttpHeaderUserAgent, DefaultUserAgent).
		SetHeader("Range", "bytes=0-0").
		Get(targetUrl)
	if res != nil && res.RawResponse != nil && res.RawResponse.Body != nil {
		defer func() {
			_ = res.RawResponse.Body.Close()
		}()
	}
	if !errors.Is(err, resty.ErrAutoRedirectDisabled) {
		return targetUrl
	}
	if locationRes, locErr := checkedLocation(SourceDouYin, res); locErr == nil && locationRes != nil {
		return locationRes.String()
	}
	return targetUrl
}

func (d douYin) getRedirectUrl(ctx context.Context, videoInfo *VideoParseInfo) {
	if videoInfo.VideoUrl == "" {
		return
	}

	// 优先通过 short_url 跟踪获取底层高优节点（如 douyinvod.com）
	if videoInfo.ShortUrl != "" {
		resolved := d.resolveRedirect(ctx, videoInfo.ShortUrl)
		if resolved != "" && !d.isHotlinkProtected(resolved) {
			videoInfo.VideoUrl = resolved
			return
		}
	}

	// 兜底逻辑：如果 short_url 解析失败，检查当前 VideoUrl 是否已经是干净的 CDN 直链
	if !d.isHotlinkProtected(videoInfo.VideoUrl) &&
		(strings.Contains(videoInfo.VideoUrl, "zjcdn.com") ||
			strings.Contains(videoInfo.VideoUrl, "douyinvod.com") ||
			strings.Contains(videoInfo.VideoUrl, "bytevcloud.com") ||
			strings.Contains(videoInfo.VideoUrl, "tos-cn-") ||
			strings.Contains(videoInfo.VideoUrl, "365yg.com")) {
		return
	}

	resolved := d.resolveRedirect(ctx, videoInfo.VideoUrl)
	if resolved != "" {
		videoInfo.VideoUrl = resolved
	}
}

func (d douYin) randSeq(n int) string {
	letters := []rune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))] // #nosec G404 -- Synthetic public slides API parameter, never used as a secret.
	}
	return string(b)
}

// 生成固定位数的随机数字（前导零）
func (d douYin) generateFixedLengthNumericID(length int) string {
	// 创建一个新的随机数生成器源
	r := rand.New(rand.NewSource(time.Now().UnixNano())) // #nosec G404 -- Synthetic public device ID, never used for authentication.

	max2 := int64(1)
	for i := 0; i < length; i++ {
		max2 *= 10
	}

	randomNum := r.Int63n(max2)
	return fmt.Sprintf("%0*d", length, randomNum)
}

// 优先获取非 .webp 格式的图片 url
func (d douYin) getNoWebpUrl(urlList []gjson.Result) string {
	var imageUrl string
	// 手动遍历查找包含 .jpeg 或 .png 的 URL
	found := false
	for _, urllink := range urlList {
		urlStr := urllink.String()
		//if strings.Contains(urlStr, ".jpeg") || strings.Contains(urlStr, ".png") {
		if !strings.Contains(urlStr, ".webp") {
			imageUrl = urlStr
			found = true
			break
		}
	}

	// 如果没找到，使用第一项
	if !found && len(urlList) > 0 {
		imageUrl = urlList[0].String()
	}

	return imageUrl
}

// 从 HTML 字符串获取 canonical URL
func (d douYin) getCanonicalFromHTML(htmlContent string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}

	return d.findCanonical(doc), nil
}

// 递归查找 canonical link
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

	// 递归遍历子节点
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if result := d.findCanonical(c); result != "" {
			return result
		}
	}

	return ""
}

func (d douYin) parseVideoIDFromSEO(ctx context.Context, videoId string) (*VideoParseInfo, error) {
	client := newClient(SourceDouYin)

	urls := []string{
		fmt.Sprintf("https://www.douyin.com/note/%s", videoId),
		fmt.Sprintf("https://www.iesdouyin.com/share/note/%s/", videoId),
		fmt.Sprintf("https://www.douyin.com/video/%s", videoId),
		fmt.Sprintf("https://www.iesdouyin.com/share/video/%s/", videoId),
	}

	spiderUA := "Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)"

	for _, reqUrl := range urls {
		res, err := client.R().SetContext(ctx).
			SetHeader(HttpHeaderUserAgent, spiderUA).
			Get(reqUrl)
		if err != nil {
			continue
		}

		info, err := d.parseSEOHTML(res.Body())
		if err == nil && info != nil {
			return info, nil
		}
	}

	return nil, errors.New("parse from seo failed")
}

func (d douYin) parseSEOHTML(htmlBytes []byte) (*VideoParseInfo, error) {
	htmlStr := string(htmlBytes)
	reScript := regexp.MustCompile(`<script[^>]*type="application/ld\+json"[^>]*>([\s\S]*?)</script>`)
	matches := reScript.FindAllStringSubmatch(htmlStr, -1)
	if len(matches) == 0 {
		reGeneric := regexp.MustCompile(`<script[^>]*>([\s\S]*?)</script>`)
		matches = reGeneric.FindAllStringSubmatch(htmlStr, -1)
	}

	for _, m := range matches {
		content := strings.TrimSpace(m[1])
		if !strings.Contains(content, "schema.org") {
			continue
		}

		title := gjson.Get(content, "headline").String()
		if title == "" {
			title = gjson.Get(content, "name").String()
		}
		if title == "" {
			continue
		}

		imageNodes := gjson.Get(content, "image").Array()
		images := make([]ImgInfo, 0, len(imageNodes))
		for _, imgItem := range imageNodes {
			imgUrl := imgItem.String()
			if imgUrl != "" {
				images = append(images, ImgInfo{
					Url: imgUrl,
				})
			}
		}

		authorName := gjson.Get(content, "author.name").String()
		authorAvatar := gjson.Get(content, "author.image").String()
		authorUrl := gjson.Get(content, "author.url").String()
		var authorUid string
		if authorUrl != "" {
			parts := strings.Split(strings.Trim(authorUrl, "/"), "/")
			if len(parts) > 0 {
				authorUid = parts[len(parts)-1]
			}
		}

		var coverUrl string
		if len(images) > 0 {
			coverUrl = images[0].Url
		} else {
			coverUrl = gjson.Get(content, "thumbnailUrl.0").String()
		}

		if len(images) == 0 && coverUrl == "" {
			continue
		}

		videoInfo := &VideoParseInfo{
			Title:    title,
			CoverUrl: coverUrl,
			Images:   images,
		}
		videoInfo.Author.Uid = authorUid
		videoInfo.Author.Name = authorName
		videoInfo.Author.Avatar = authorAvatar
		return videoInfo, nil
	}

	return nil, errors.New("no matching schema.org json found")
}
