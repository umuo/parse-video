package parser

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

var errUntrustedTarget = errors.New("outbound URL is not allowed")
var errShortLinkDidNotRedirect = errors.New("short link did not redirect")

// Additional hosts used for platform APIs, short-link destinations and media probes.
var upstreamDomains = map[string][]string{
	SourceDouYin: {
		"ixigua.com",
		"aweme.snssdk.com",
		"douyinvod.com",
		"ttwid.bytedance.com",
		"zjcdn.com",
		"bytevcloud.com",
		"bytevcloudcdn.com",
		"365yg.com",
		"amemv.com",
		"volces.com",
	},
	SourceKuaiShou:    {"v.m.chenzhongtech.com"},
	SourceXiGua:       {"ixigua.com"},
	SourcePiPiXia:     {"api.pipix.com"},
	SourceWeiShi:      {"h5.weishi.qq.com"},
	SourceHuoShan:     {"huoshan.com"},
	SourcePiPiGaoXiao: {"share.ippzone.com"},
	SourceQuanMin:     {"quanmin.hao222.com"},
	SourceHuYa:        {"liveapi.huya.com"},
	SourceWeiBo:       {"m.weibo.cn"},
	SourceRedBook:     {"xiaohongshu.com"},
	SourceTwitter:     {"cdn.syndication.twimg.com"},
	SourceQQVideo:     {"vv.video.qq.com"},
	SourceSohu:        {"api.tv.sohu.com"},
	SourceCCTV:        {"vdn.apps.cntv.cn"},
}

func httpHostname(target *url.URL) string {
	if target.User != nil || target.Opaque != "" || strings.HasSuffix(target.Host, ":") {
		return ""
	}
	port := target.Port()
	switch target.Scheme {
	case "http":
		if port != "" && port != "80" {
			return ""
		}
	case "https":
		if port != "" && port != "443" {
			return ""
		}
	default:
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
}

func hostMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func outboundURLAllowed(source string, target *url.URL) bool {
	host := httpHostname(target)
	if host == "" {
		return false
	}
	for _, domains := range [][]string{videoSourceInfoMapping[source].VideoShareUrlDomain, upstreamDomains[source]} {
		for _, domain := range domains {
			if hostMatches(host, domain) {
				return true
			}
		}
	}
	return false
}

type checkedTransport struct {
	*http.Transport
	allows func(*url.URL) bool
}

func (t checkedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.allows(req.URL) {
		return nil, errUntrustedTarget
	}
	return t.Transport.RoundTrip(req)
}

func checkedLocation(source string, response *resty.Response) (*url.URL, error) {
	if response == nil || response.RawResponse == nil {
		return nil, errors.New("missing upstream response")
	}
	location, err := response.RawResponse.Location()
	if err != nil {
		return nil, err
	}
	if !outboundURLAllowed(source, location) {
		return nil, errUntrustedTarget
	}
	return location, nil
}

const maxRedirects = 10

var stripRedirectCredentials = resty.RedirectPolicyFunc(func(req *http.Request, via []*http.Request) error {
	origin := via[0].URL
	if req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) {
		for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			req.Header.Del(name)
		}
	} else if previous := via[len(via)-1].URL; req.URL.Scheme != previous.Scheme || !strings.EqualFold(req.URL.Host, previous.Host) {
		// net/http can generate Referer from the previous hop even when returning to the initial origin.
		req.Header.Del("Referer")
	}
	return nil
})

var globalIPv6 = netip.MustParsePrefix("2000::/3")

// Special-purpose ranges outside the private, loopback and link-local checks.
var specialAddresses = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"), netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !globalIPv6.Contains(ip)) {
		return false
	}
	for _, prefix := range specialAddresses {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func publicDialContext(
	lookup func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		const dialTimeout = 30 * time.Second
		ctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errors.New("upstream hostname resolved to no addresses")
		}
		for _, ip := range addresses {
			if !publicAddress(ip) {
				return nil, errors.New("upstream hostname resolved to a non-public address")
			}
		}
		attemptTimeout := max(2*time.Second, dialTimeout/time.Duration(len(addresses)))
		for _, ip := range addresses {
			attemptCtx, finish := context.WithTimeout(ctx, attemptTimeout)
			var conn net.Conn
			conn, err = dial(attemptCtx, network, net.JoinHostPort(ip.Unmap().String(), port))
			finish()
			if err == nil {
				return conn, nil
			}
		}
		return nil, err
	}
}
