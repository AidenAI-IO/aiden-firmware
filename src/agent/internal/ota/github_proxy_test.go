package ota

import "testing"

func TestApplyGitHubProxy(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		proxyURL string
		want     string
	}{
		{
			name:     "no proxy configured",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "",
			want:     "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "gh-proxy.com with trailing slash",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "https://gh-proxy.com/",
			want:     "https://gh-proxy.com/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "gh-proxy.com without trailing slash",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "https://gh-proxy.com",
			want:     "https://gh-proxy.com/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "ghfast.top proxy",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "https://ghfast.top/",
			want:     "https://ghfast.top/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "custom proxy",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "https://my-proxy.example.com/",
			want:     "https://my-proxy.example.com/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "api.github.com URL",
			url:      "https://api.github.com/repos/owner/repo/releases/latest",
			proxyURL: "https://gh-proxy.com/",
			want:     "https://gh-proxy.com/https://api.github.com/repos/owner/repo/releases/latest",
		},
		{
			name:     "raw.githubusercontent.com URL",
			url:      "https://raw.githubusercontent.com/owner/repo/main/file.txt",
			proxyURL: "https://gh-proxy.com/",
			want:     "https://gh-proxy.com/https://raw.githubusercontent.com/owner/repo/main/file.txt",
		},
		{
			name:     "objects.githubusercontent.com URL",
			url:      "https://objects.githubusercontent.com/github-production-release-asset-123/file.tar.gz",
			proxyURL: "https://gh-proxy.com/",
			want:     "https://gh-proxy.com/https://objects.githubusercontent.com/github-production-release-asset-123/file.tar.gz",
		},
		{
			name:     "non-GitHub URL unchanged",
			url:      "https://example.com/file.tar.gz",
			proxyURL: "https://gh-proxy.com/",
			want:     "https://example.com/file.tar.gz",
		},
		{
			name:     "whitespace in proxy URL",
			url:      "https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
			proxyURL: "  https://gh-proxy.com/  ",
			want:     "https://gh-proxy.com/https://github.com/owner/repo/releases/download/v1.0.0/file.tar.gz",
		},
		{
			name:     "empty GitHub URL",
			url:      "",
			proxyURL: "https://gh-proxy.com/",
			want:     "",
		},
		{
			name:     "whitespace-only GitHub URL",
			url:      "   ",
			proxyURL: "https://gh-proxy.com/",
			want:     "",
		},
		{
			name:     "proxy URL with path",
			url:      "https://github.com/owner/repo/file.tar.gz",
			proxyURL: "https://my-proxy.com/github-mirror/",
			want:     "https://my-proxy.com/github-mirror/https://github.com/owner/repo/file.tar.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyGitHubProxy(tt.url, tt.proxyURL)
			if got != tt.want {
				t.Errorf("ApplyGitHubProxy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsGitHubURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"github.com", "https://github.com/owner/repo", true},
		{"api.github.com", "https://api.github.com/repos/owner/repo", true},
		{"raw.githubusercontent.com", "https://raw.githubusercontent.com/owner/repo/main/file", true},
		{"objects.githubusercontent.com", "https://objects.githubusercontent.com/asset/file", true},
		{"non-GitHub", "https://example.com/file", false},
		{"HTTP github", "http://github.com/repo", false}, // Only HTTPS
		{"case insensitive", "HTTPS://GITHUB.COM/repo", true},
		{"gitlab", "https://gitlab.com/owner/repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGitHubURL(tt.url)
			if got != tt.want {
				t.Errorf("isGitHubURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
