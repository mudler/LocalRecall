package sources

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/mudler/xlog"
	sitemap "github.com/oxffaa/gopher-parse-sitemap"
	"jaytaylor.com/html2text"
)

// isDisallowedIP reports whether ip belongs to a range that server-side
// fetches must not reach: loopback, private (RFC 1918 / ULA), link-local
// (including the 169.254.169.254 cloud metadata endpoint), the unspecified
// address and multicast. This keeps the URL-ingestion feature from being
// turned into a server-side request forgery primitive against the host and
// the internal network.
func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// safeHTTPClient dials only public addresses. The Control callback runs after
// DNS resolution with the concrete address that will be connected to, so it
// also rejects hostnames that resolve (or re-bind) to a disallowed IP.
var safeHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 30 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil || isDisallowedIP(ip) {
					return fmt.Errorf("refusing to connect to disallowed address: %s", address)
				}
				return nil
			},
		}).DialContext,
	},
}

// assertFetchableURL rejects URLs whose scheme is not http/https or whose host
// already resolves exclusively to disallowed addresses. The dialer above is the
// authoritative guard against DNS rebinding; this gives an early, clear error.
func assertFetchableURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host in URL")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedIP(ip) {
			return fmt.Errorf("refusing to fetch disallowed address: %s", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !isDisallowedIP(ip) {
			return nil
		}
	}
	return fmt.Errorf("refusing to fetch host that resolves only to disallowed addresses: %s", host)
}

func GetWebPage(pageURL string) (string, error) {
	if err := assertFetchableURL(pageURL); err != nil {
		return "", err
	}

	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", err
	}

	// Set User-Agent to avoid being blocked by websites like Wikipedia
	req.Header.Set("User-Agent", "LocalRecall/1.0 (https://github.com/mudler/localrecall)")

	resp, err := safeHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP error: %d %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	text, err := html2text.FromString(string(body), html2text.Options{PrettyTables: true})
	if err != nil {
		return "", fmt.Errorf("failed to convert HTML to text: %w", err)
	}

	if len(text) < 100 {
		// Very short content might indicate an error page or blocking
		xlog.Warn("Very short content extracted from URL", "url", pageURL, "length", len(text), "html_length", len(body))
	}

	return text, nil
}

func GetWebSitemapContent(sitemapURL string) (res []string, err error) {
	if err = assertFetchableURL(sitemapURL); err != nil {
		return nil, err
	}
	err = sitemap.ParseFromSite(sitemapURL, func(e sitemap.Entry) error {
		xlog.Info("Sitemap page: " + e.GetLocation())
		content, err := GetWebPage(e.GetLocation())
		if err == nil {
			res = append(res, content)
		}
		return nil
	})
	return
}
