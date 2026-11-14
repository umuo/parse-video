package parser

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// NewMediaClient streams media from the registrable domains in a parse result.
// Every redirect uses the same domain and network policy as the initial request.
func NewMediaClient(mediaURLs []string) *http.Client {
	domains := make(map[string]bool)
	for _, rawURL := range mediaURLs {
		if target, err := url.Parse(rawURL); err == nil {
			if domain := mediaDomain(target); domain != "" {
				domains[domain] = true
			}
		}
	}
	client := newOutboundClient().GetClient()
	transport := client.Transport.(*http.Transport)
	transport.ResponseHeaderTimeout = 20 * time.Second
	client.Timeout = 10 * time.Minute
	client.Transport = checkedTransport{
		Transport: transport,
		allows:    func(target *url.URL) bool { return domains[mediaDomain(target)] },
	}
	return client
}

func mediaDomain(target *url.URL) string {
	host := httpHostname(target)
	if host == "" {
		return ""
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return ""
	}
	host, err := idna.Lookup.ToASCII(host)
	if err != nil || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return ""
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann && !strings.Contains(suffix, ".") {
		return ""
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	return domain
}
