package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ArkClient is a small OpenAI-compatible client for Volcengine Ark LLM calls.
type ArkClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// defaultArkRequestTimeout 是 Ark 同步请求的默认 HTTP 超时。
const defaultArkRequestTimeout = 90 * time.Second

func NewArkClient(baseURL string, apiKey string) *ArkClient {
	return NewArkClientWithTimeout(baseURL, apiKey, 0)
}

// NewArkClientWithTimeout 创建指定 HTTP 超时的客户端；timeout <= 0 时使用默认 90s。
// 长文本分析（如章节 txt 导入）应传更长的超时，避免 Client.Timeout 提前中断请求。
func NewArkClientWithTimeout(baseURL string, apiKey string, timeout time.Duration) *ArkClient {
	if timeout <= 0 {
		timeout = defaultArkRequestTimeout
	}
	return &ArkClient{
		BaseURL: strings.TrimSpace(baseURL),
		APIKey:  strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type responsesRequest struct {
	Model string               `json:"model"`
	Input []responsesInputItem `json:"input"`
}

type responsesInputItem struct {
	Role    string                 `json:"role"`
	Content []responsesContentItem `json:"content"`
}

type responsesContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesOutput struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Choices []struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *ArkClient) GenerateText(ctx context.Context, modelID string, systemPrompt string, userPrompt string) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", fmt.Errorf("ark api key is not configured")
	}
	if strings.TrimSpace(modelID) == "" {
		return "", fmt.Errorf("model id is required")
	}
	if strings.TrimSpace(userPrompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	input := make([]responsesInputItem, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		input = append(input, responsesInputItem{
			Role:    "system",
			Content: []responsesContentItem{{Type: "input_text", Text: strings.TrimSpace(systemPrompt)}},
		})
	}
	input = append(input, responsesInputItem{
		Role:    "user",
		Content: []responsesContentItem{{Type: "input_text", Text: strings.TrimSpace(userPrompt)}},
	})

	return c.createResponsesText(ctx, responsesRequest{
		Model: strings.TrimSpace(modelID),
		Input: input,
	})
}

func (c *ArkClient) GenerateTextFromImage(ctx context.Context, modelID string, imageURL string, prompt string) (string, error) {
	if strings.TrimSpace(imageURL) == "" {
		return "", fmt.Errorf("image url is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	return c.createResponsesText(ctx, responsesRequest{
		Model: strings.TrimSpace(modelID),
		Input: []responsesInputItem{{
			Role: "user",
			Content: []responsesContentItem{
				{Type: "input_image", ImageURL: strings.TrimSpace(imageURL)},
				{Type: "input_text", Text: prompt},
			},
		}},
	})
}

func (c *ArkClient) GenerateTextWithImages(ctx context.Context, modelID string, systemPrompt string, userPrompt string, imageURLs []string) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", fmt.Errorf("ark api key is not configured")
	}
	if strings.TrimSpace(modelID) == "" {
		return "", fmt.Errorf("model id is required")
	}
	if strings.TrimSpace(userPrompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}

	input := make([]responsesInputItem, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		input = append(input, responsesInputItem{
			Role:    "system",
			Content: []responsesContentItem{{Type: "input_text", Text: strings.TrimSpace(systemPrompt)}},
		})
	}

	content := []responsesContentItem{{Type: "input_text", Text: strings.TrimSpace(userPrompt)}}
	for _, imageURL := range imageURLs {
		trimmed := strings.TrimSpace(imageURL)
		if trimmed == "" {
			continue
		}
		content = append(content, responsesContentItem{Type: "input_image", ImageURL: trimmed})
	}
	input = append(input, responsesInputItem{Role: "user", Content: content})

	return c.createResponsesText(ctx, responsesRequest{
		Model: strings.TrimSpace(modelID),
		Input: input,
	})
}

func (c *ArkClient) createResponsesText(ctx context.Context, payload responsesRequest) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", fmt.Errorf("ark api key is not configured")
	}
	if strings.TrimSpace(payload.Model) == "" {
		return "", fmt.Errorf("model id is required")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = resp.Status
		}
		return "", fmt.Errorf("ark responses request failed: %s", message)
	}

	var parsed responsesOutput
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode ark responses payload: %v (raw head: %s)", err, truncateRunes(string(raw), 300))
	}
	text := extractResponsesText(parsed)
	if text == "" {
		return "", fmt.Errorf("ark responses output is empty")
	}
	return text, nil
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func (c *ArkClient) responsesURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	if strings.HasSuffix(base, "/v3") {
		return base + "/responses"
	}
	return base + "/v3/responses"
}

func extractResponsesText(resp responsesOutput) string {
	if text := strings.TrimSpace(resp.OutputText); text != "" {
		return text
	}
	for _, output := range resp.Output {
		for _, content := range output.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				return text
			}
		}
	}
	for _, choice := range resp.Choices {
		switch value := choice.Message.Content.(type) {
		case string:
			if text := strings.TrimSpace(value); text != "" {
				return text
			}
		case []any:
			for _, item := range value {
				obj, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := obj["text"].(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}
