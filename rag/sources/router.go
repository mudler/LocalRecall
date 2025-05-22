package sources

import (
	"strings"

	"github.com/mudler/localrecall/pkg/xlog"
)

func SourceRouter(url string, config *Config) (string, error) {
	xlog.Info("Downloading content from", "url", url)

	switch {
	case strings.HasSuffix(url, ".git"):
		content, err := GetGitRepositoryContent(url, config.GitPrivateKey)
		if err != nil {
			return "", err
		}
		xlog.Info("Downloaded content from Git repository", "url", url)
		return content, nil
	case strings.HasSuffix(url, "sitemap.xml"):
		content, err := GetWebSitemapContent(url)
		if err != nil {
			return "", err
		}
		xlog.Info("Downloaded all content from sitemap", "url", url, "length", len(content))
		return strings.Join(content, "\n"), nil
	default:
		// Default to web page
		return GetWebPage(url)
	}
}
