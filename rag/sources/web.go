package sources

import (
	"io"
	"net/http"

	"github.com/mudler/xlog"
	sitemap "github.com/oxffaa/gopher-parse-sitemap"
	"jaytaylor.com/html2text"
)

func GetWebPage(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return html2text.FromString(string(body), html2text.Options{PrettyTables: true})
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
