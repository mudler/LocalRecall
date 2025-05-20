package sources

import "strings"

func SourceRouter(url string) (string, error) {

	switch {
	case strings.HasSuffix(url, "sitemap.xml"):
		content, err := GetWebSitemapContent(url)
		if err != nil {
			return "", err
		}
		return strings.Join(content, "\n"), nil
	}

	return GetWebPage(url)
}
