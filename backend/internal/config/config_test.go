package config

import (
	"testing"
	"time"
)

func TestParseTimeoutSeconds(t *testing.T) {
	cases := []struct {
		raw      string
		fallback time.Duration
		want     time.Duration
	}{
		{"", 5 * time.Minute, 5 * time.Minute},
		{"  ", 5 * time.Minute, 5 * time.Minute},
		{"5m", 90 * time.Second, 5 * time.Minute},
		{"90s", 5 * time.Minute, 90 * time.Second},
		{"300", 90 * time.Second, 300 * time.Second},
		{"0", 5 * time.Minute, 5 * time.Minute},
		{"-5s", 5 * time.Minute, 5 * time.Minute},
		{"abc", 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		if got := parseTimeoutSeconds(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("parseTimeoutSeconds(%q, %v) = %v, want %v", tc.raw, tc.fallback, got, tc.want)
		}
	}
}
