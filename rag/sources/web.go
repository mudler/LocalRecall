package sources

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mudler/xlog"
	sitemap "github.com/oxffaa/gopher-parse-sitemap"
	"jaytaylor.com/html2text"
)

func GetWebPage(url string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	// Set User-Agent to avoid being blocked by websites like Wikipedia
	req.Header.Set("User-Agent", "LocalRecall/1.0 (https://github.com/mudler/localrecall)")

	resp, err := client.Do(req)
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
		xlog.Warn("Very short content extracted from URL", "url", url, "length", len(text), "html_length", len(body))
	}

	return text, nil
}

func GetWebSitemapContent(url string) (res []string, err error) {
	err = sitemap.ParseFromSite(url, func(e sitemap.Entry) error {
		xlog.Info("Sitemap page: " + e.GetLocation())
		content, err := GetWebPage(e.GetLocation())
		if err == nil {
			res = append(res, content)
		}
		return nil
	})
	return
}
