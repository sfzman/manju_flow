package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"manju-flow/internal/models"
)

func TestBuildFinalGenerationPromptAppendsArtStyle(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		artStyle       string
		appendArtStyle bool
		expected       string
	}{
		{
			name:           "appends art style when enabled",
			text:           "少女在雨中奔跑",
			artStyle:       "日系赛璐璐动画风格，柔和光影",
			appendArtStyle: true,
			expected:       "少女在雨中奔跑\n日系赛璐璐动画风格，柔和光影",
		},
		{
			name:           "does not append when disabled",
			text:           "少女在雨中奔跑",
			artStyle:       "日系赛璐璐动画风格",
			appendArtStyle: false,
			expected:       "少女在雨中奔跑",
		},
		{
			name:           "does not append empty art style",
			text:           "少女在雨中奔跑",
			artStyle:       "   ",
			appendArtStyle: true,
			expected:       "少女在雨中奔跑",
		},
		{
			name:           "trims art style before appending",
			text:           "少女在雨中奔跑",
			artStyle:       "  水彩质感  ",
			appendArtStyle: true,
			expected:       "少女在雨中奔跑\n水彩质感",
		},
		{
			name:           "keeps text unchanged when both missing",
			text:           "少女在雨中奔跑",
			artStyle:       "",
			appendArtStyle: false,
			expected:       "少女在雨中奔跑",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildFinalGenerationPrompt(tc.text, tc.artStyle, tc.appendArtStyle)
			if got != tc.expected {
				t.Errorf("buildFinalGenerationPrompt() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestBuildFinalGenerationPromptKeepsUserPromptIntact(t *testing.T) {
	text := "镜头缓缓推近\n主角抬起头"
	got := buildFinalGenerationPrompt(text, "3D 渲染", true)
	if !strings.HasPrefix(got, text) {
		t.Errorf("user prompt should stay at the front, got %q", got)
	}
	if !strings.HasSuffix(got, "3D 渲染") {
		t.Errorf("art style should be appended at the end, got %q", got)
	}
}

func TestResolveAppendArtStyleDefaultsToEnabled(t *testing.T) {
	if !resolveAppendArtStyle(nil) {
		t.Error("omitted appendArtStyle should default to enabled")
	}
	enabled := true
	if !resolveAppendArtStyle(&enabled) {
		t.Error("explicit true should stay enabled")
	}
	disabled := false
	if resolveAppendArtStyle(&disabled) {
		t.Error("explicit false should stay disabled")
	}
}

func TestGenerateSceneAnimationRequestParsesOmittedAppendArtStyle(t *testing.T) {
	var req models.GenerateSceneAnimationRequest
	if err := json.Unmarshal([]byte(`{"text":"t","ratio":"16:9","duration":8,"model":"wan3.0-video"}`), &req); err != nil {
		t.Fatalf("failed to unmarshal request: %v", err)
	}
	if req.AppendArtStyle != nil {
		t.Errorf("omitted appendArtStyle should unmarshal to nil, got %v", *req.AppendArtStyle)
	}
	if !resolveAppendArtStyle(req.AppendArtStyle) {
		t.Error("omitted appendArtStyle should resolve to enabled")
	}
}

func TestValidateAnimationDuration(t *testing.T) {
	cases := []struct {
		name          string
		model         string
		duration      int
		expectedError bool
	}{
		{name: "wan smart duration", model: "wan3.0-video", duration: -1, expectedError: false},
		{name: "wan prime smart duration", model: "wan3.0-video-prime", duration: -1, expectedError: false},
		{name: "wan lower bound", model: "wan3.0-video", duration: 2, expectedError: false},
		{name: "wan upper bound", model: "wan3.0-video", duration: 30, expectedError: false},
		{name: "wan below lower bound", model: "wan3.0-video", duration: 1, expectedError: true},
		{name: "wan above upper bound", model: "wan3.0-video", duration: 31, expectedError: true},
		{name: "wan other negative rejected", model: "wan3.0-video", duration: -2, expectedError: true},
		{name: "ark rejects smart duration", model: "doubao-seedance-2-5-flash-261115", duration: -1, expectedError: true},
		{name: "ark lower bound", model: "doubao-seedance-2-5-flash-261115", duration: 4, expectedError: false},
		{name: "ark upper bound", model: "doubao-seedance-2-5-flash-261115", duration: 30, expectedError: false},
		{name: "ark below lower bound", model: "doubao-seedance-2-5-flash-261115", duration: 3, expectedError: true},
		{name: "ark above upper bound", model: "doubao-seedance-2-5-flash-261115", duration: 31, expectedError: true},
		{name: "legacy ark model rejected", model: "doubao-seedance-2-0-260128", duration: 8, expectedError: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAnimationDuration(tc.model, tc.duration)
			if tc.expectedError && err == nil {
				t.Errorf("expected error for model=%s duration=%d, got nil", tc.model, tc.duration)
			}
			if !tc.expectedError && err != nil {
				t.Errorf("unexpected error for model=%s duration=%d: %v", tc.model, tc.duration, err)
			}
		})
	}
}
