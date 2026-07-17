package ota

import (
	"strings"
)

// ApplyGitHubProxy transforms a GitHub URL through a proxy service if configured.
// Supported proxy services rewrite github.com URLs to accelerate downloads in regions
// with poor GitHub connectivity.
//
// Examples:
//   - https://gh-proxy.com/ prepends to the full URL
//   - https://ghfast.top/ prepends to the full URL
//   - Custom proxies follow the same pattern
//
// If proxyURL is empty or url is not a GitHub URL, returns url unchanged.
func ApplyGitHubProxy(url string, proxyURL string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return url
	}

	// Only apply proxy to GitHub URLs
	if !isGitHubURL(url) {
		return url
	}

	// Ensure proxy URL ends with / for clean concatenation
	if !strings.HasSuffix(proxyURL, "/") {
		proxyURL += "/"
	}

	// Proxy services expect the full GitHub URL after their base
	return proxyURL + url
}

// isGitHubURL checks if the URL is from GitHub domains that benefit from proxy services.
func isGitHubURL(url string) bool {
	// Match both api.github.com and github.com (for release assets)
	url = strings.ToLower(url)
	return strings.HasPrefix(url, "https://github.com/") ||
		strings.HasPrefix(url, "https://api.github.com/") ||
		strings.HasPrefix(url, "https://raw.githubusercontent.com/") ||
		strings.HasPrefix(url, "https://objects.githubusercontent.com/")
}
