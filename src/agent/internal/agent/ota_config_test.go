package agent

import (
	"strings"
	"testing"
)

func TestOTAConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  OTAConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty URL (no proxy)",
			config:  OTAConfig{GitHubProxyURL: ""},
			wantErr: false,
		},
		{
			name:    "valid HTTPS proxy",
			config:  OTAConfig{GitHubProxyURL: "https://gh-proxy.com/"},
			wantErr: false,
		},
		{
			name:    "valid HTTPS proxy without trailing slash",
			config:  OTAConfig{GitHubProxyURL: "https://ghfast.top"},
			wantErr: false,
		},
		{
			name:    "whitespace is trimmed",
			config:  OTAConfig{GitHubProxyURL: "  https://gh-proxy.com/  "},
			wantErr: false,
		},
		{
			name:    "HTTP instead of HTTPS",
			config:  OTAConfig{GitHubProxyURL: "http://gh-proxy.com/"},
			wantErr: true,
			errMsg:  "must use https",
		},
		{
			name:    "invalid URL format",
			config:  OTAConfig{GitHubProxyURL: "not-a-url"},
			wantErr: true,
			errMsg:  "absolute URL",
		},
		{
			name:    "missing host",
			config:  OTAConfig{GitHubProxyURL: "https://"},
			wantErr: true,
			errMsg:  "missing host",
		},
		{
			name:    "no scheme",
			config:  OTAConfig{GitHubProxyURL: "gh-proxy.com"},
			wantErr: true,
			errMsg:  "absolute URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.errMsg)
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error = %v", err)
				}
			}
		})
	}
}
