package parser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/wujunwei928/parse-video/utils"
)

const parseTimeout = 30 * time.Second

// ParseVideoShareUrlByRegexp 将分享链接信息, 进行正则表达式匹配到分享链接后, 再解析视频信息
func ParseVideoShareUrlByRegexp(shareMsg string) (*VideoParseInfo, error) {
	return ParseVideoShareUrlByRegexpContext(context.Background(), shareMsg)
}

// ParseVideoShareUrlByRegexpContext extracts and parses a share URL with cancellation.
func ParseVideoShareUrlByRegexpContext(ctx context.Context, shareMsg string) (*VideoParseInfo, error) {
	videoShareUrl, err := utils.RegexpMatchUrlFromString(shareMsg)
	if err != nil {
		return nil, err
	}

	return ParseVideoShareUrlContext(ctx, videoShareUrl)
}

// ParseVideoShareUrl 根据视频分享链接解析视频信息: 分享链接需是正常http链接
func ParseVideoShareUrl(shareUrl string) (*VideoParseInfo, error) {
	return ParseVideoShareUrlContext(context.Background(), shareUrl)
}

// ParseVideoShareUrlContext parses a share URL within a single request budget.
func ParseVideoShareUrlContext(ctx context.Context, shareUrl string) (*VideoParseInfo, error) {
	source := shareURLSource(shareUrl)
	if source == "" {
		return nil, errors.New("share URL does not match a supported platform")
	}

	// 没有对应的视频链接解析方法
	urlParser := videoSourceInfoMapping[source].VideoShareUrlParser
	if urlParser == nil {
		return nil, fmt.Errorf("source %s has no video share url parser", source)
	}

	return parseWithTimeout(ctx, urlParser.parseShareUrl, shareUrl)
}

// IsSupportedShareURL reports whether the URL matches a platform's share hosts.
func IsSupportedShareURL(rawURL string) bool {
	return shareURLSource(rawURL) != ""
}

func shareURLSource(rawURL string) string {
	target, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := httpHostname(target)
	if host == "" {
		return ""
	}
	var source string
	var longest int
	for name, info := range videoSourceInfoMapping {
		for _, domain := range info.VideoShareUrlDomain {
			if len(domain) > longest && hostMatches(host, domain) {
				source, longest = name, len(domain)
			}
		}
	}
	return source
}

// ParseVideoId 根据视频id解析视频信息
func ParseVideoId(source, videoId string) (*VideoParseInfo, error) {
	return ParseVideoIdContext(context.Background(), source, videoId)
}

// ParseVideoIdContext parses a platform video ID with cancellation.
func ParseVideoIdContext(ctx context.Context, source, videoId string) (*VideoParseInfo, error) {
	if len(videoId) <= 0 || len(source) <= 0 {
		return nil, errors.New("video id or source is empty")
	}

	idParser := videoSourceInfoMapping[source].VideoIdParser
	if idParser == nil {
		return nil, fmt.Errorf("source %s has no video id parser", source)
	}

	return parseWithTimeout(ctx, idParser.parseVideoID, videoId)
}

func parseWithTimeout(ctx context.Context, parse func(context.Context, string) (*VideoParseInfo, error), input string) (*VideoParseInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, parseTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := parse(ctx, input)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return info, err
}

// BatchParseVideoId 根据视频id批量解析视频信息
func BatchParseVideoId(source string, videoIds []string) (map[string]BatchParseItem, error) {
	if len(videoIds) <= 0 || len(source) <= 0 {
		return nil, errors.New("videos id or source is empty")
	}

	idParser := videoSourceInfoMapping[source].VideoIdParser
	if idParser == nil {
		return nil, fmt.Errorf("source %s has no video id parser", source)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	parseMap := make(map[string]BatchParseItem, len(videoIds))
	for _, v := range videoIds {
		wg.Add(1)
		videoId := v
		go func(videoId string) {
			defer wg.Done()

			parseInfo, parseErr := ParseVideoId(source, videoId)
			mu.Lock()
			parseMap[videoId] = BatchParseItem{
				ParseInfo: parseInfo,
				Error:     parseErr,
			}
			mu.Unlock()
		}(videoId)
	}
	wg.Wait()

	return parseMap, nil
}
