package parser

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/go-resty/resty/v2"
)

var proxyURL atomic.Value // 存储 string

const maxParseResponseBytes = 16 << 20

// InitProxy 初始化代理配置，校验 URL 合法性。
// 空字符串表示不使用代理。应在程序启动时调用。
func InitProxy(proxy string) error {
	if proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("PARSE_VIDEO_PROXY 必须是有效的 HTTP 或 HTTPS 代理地址")
		}
	}
	proxyURL.Store(proxy)
	return nil
}

func newOutboundClient() *resty.Client {
	client := resty.New()
	transport := client.GetClient().Transport.(*http.Transport)
	if proxy, ok := proxyURL.Load().(string); ok && proxy != "" {
		client.SetProxy(proxy)
	} else {
		transport.Proxy = nil
		transport.DialContext = publicDialContext(net.DefaultResolver.LookupNetIP, transport.DialContext)
	}
	client.SetRedirectPolicy(resty.FlexibleRedirectPolicy(maxRedirects), stripRedirectCredentials)
	return client
}

// newClient 创建受平台域名约束的解析客户端。
func newClient(source string) *resty.Client {
	client := newOutboundClient()
	client.SetTransport(checkedTransport{
		Transport: client.GetClient().Transport.(*http.Transport),
		allows:    func(target *url.URL) bool { return outboundURLAllowed(source, target) },
	})
	client.SetResponseBodyLimit(maxParseResponseBytes)
	return client
}
