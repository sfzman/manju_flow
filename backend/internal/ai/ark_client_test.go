package ai

import (
	"testing"
	"time"
)

func TestNewArkClientUsesDefaultTimeout(t *testing.T) {
	client := NewArkClient("https://ark.cn-beijing.volces.com/api/plan", "test-key")
	if got := client.HTTPClient.Timeout; got != defaultArkRequestTimeout {
		t.Fatalf("NewArkClient() timeout = %v, want default %v", got, defaultArkRequestTimeout)
	}
}

func TestNewArkClientWithTimeoutOverrides(t *testing.T) {
	client := NewArkClientWithTimeout("https://ark.cn-beijing.volces.com/api/plan", "test-key", 5*time.Minute)
	if got := client.HTTPClient.Timeout; got != 5*time.Minute {
		t.Fatalf("NewArkClientWithTimeout() timeout = %v, want 5m", got)
	}

	// timeout <= 0 回退默认值
	fallback := NewArkClientWithTimeout("https://ark.cn-beijing.volces.com/api/plan", "test-key", 0)
	if got := fallback.HTTPClient.Timeout; got != defaultArkRequestTimeout {
		t.Fatalf("NewArkClientWithTimeout(0) timeout = %v, want default %v", got, defaultArkRequestTimeout)
	}
}

func TestResponsesURLAppendsVersionedPath(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"", "https://ark.cn-beijing.volces.com/api/v3/responses"},
		{"https://ark.cn-beijing.volces.com/api/plan", "https://ark.cn-beijing.volces.com/api/plan/v3/responses"},
		{"https://ark.cn-beijing.volces.com/api/plan/", "https://ark.cn-beijing.volces.com/api/plan/v3/responses"},
		{"https://ark.cn-beijing.volces.com/api/plan/v3", "https://ark.cn-beijing.volces.com/api/plan/v3/responses"},
		{"https://example.com/responses", "https://example.com/responses"},
	}
	for _, tc := range cases {
		client := &ArkClient{BaseURL: tc.base}
		if got := client.responsesURL(); got != tc.want {
			t.Errorf("responsesURL(base=%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}
