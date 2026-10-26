package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"manju-flow/internal/ai"
	"manju-flow/internal/config"
	"manju-flow/internal/database"
	"manju-flow/internal/models"
	"manju-flow/internal/oss"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	arkTaskCreatePath = "/v3/contents/generations/tasks"
	wanTaskCreatePath = "/api/v1/services/aigc/video-generation/video-synthesis"
	wanTaskQueryPath  = "/api/v1/tasks"

	animationTaskPollInterval       = 10 * time.Second
	animationTaskPollBatchSize      = 20
	animationTaskPollRequestTimeout = 30 * time.Second
	animationVersionDownloadTimeout = 2 * time.Minute

	// animationPromptLLMTimeout 是生成分镜草稿/一键优化提示词的 LLM 同步调用超时；
	// 输入包含场景上下文与多张参考图，耗时可能远超普通请求，与章节导入保持 5 分钟。
	animationPromptLLMTimeout = 5 * time.Minute

	HeaderDashScopeAsync = "X-Dashscope-Async"
)

var (
	allowedAnimationRatios = map[string]struct{}{
		"16:9": {},
		"9:16": {},
	}
	allowedAnimationModels = map[string]struct{}{
		// 下面是agent plan 的model id
		// "doubao-seedance-2.0":      {},
		// "doubao-seedance-2.0-fast": {},
		"doubao-seedance-2-0-260128":      {},
		"doubao-seedance-2-0-fast-260128": {},
		"wan3.0-video":                    {},
		"wan3.0-video-prime":              {},
	}
	animationTaskPollerRunning atomic.Bool
)

// AnimationHandler 动画处理器
type AnimationHandler struct{}

// NewAnimationHandler 创建动画处理器
func NewAnimationHandler() *AnimationHandler {
	return &AnimationHandler{}
}

type arkMediaURL struct {
	URL string `json:"url"`
}

type arkGenerationContentItem struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *arkMediaURL `json:"image_url,omitempty"`
	VideoURL *arkMediaURL `json:"video_url,omitempty"`
	AudioURL *arkMediaURL `json:"audio_url,omitempty"`
	Role     string       `json:"role,omitempty"`
}

type arkGenerationRequest struct {
	Model         string                     `json:"model"`
	Content       []arkGenerationContentItem `json:"content"`
	GenerateAudio bool                       `json:"generate_audio"`
	Ratio         string                     `json:"ratio"`
	Duration      int                        `json:"duration"`
	Watermark     bool                       `json:"watermark"`
}

type arkTaskCreateResponse struct {
	ID      string `json:"id"`
	Error   any    `json:"error"`
	Message string `json:"message"`
}

type arkTaskStatusResponse struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Error     any    `json:"error"`
	Message   string `json:"message"`
	LastError any    `json:"last_error"`
	Content   struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
}

type wanMediaItem struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type wanGenerationRequest struct {
	Model string `json:"model"`
	Input struct {
		Prompt string         `json:"prompt"`
		Media  []wanMediaItem `json:"media,omitempty"`
	} `json:"input"`
	Parameters struct {
		Resolution   string `json:"resolution"`
		Ratio        string `json:"ratio"`
		Duration     int    `json:"duration"`
		Audio        bool   `json:"audio"`
		PromptExtend bool   `json:"prompt_extend"`
		Watermark    bool   `json:"watermark"`
	} `json:"parameters"`
}

type wanTaskCreateResponse struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		Code       string `json:"code"`
		Message    string `json:"message"`
	} `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type wanTaskStatusResponse struct {
	Output struct {
		TaskID     string `json:"task_id"`
		TaskStatus string `json:"task_status"`
		VideoURL   string `json:"video_url"`
		Code       string `json:"code"`
		Message    string `json:"message"`
	} `json:"output"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type arkHTTPError struct {
	StatusCode int
	Message    string
}

func isWanAnimationModel(model string) bool {
	switch strings.TrimSpace(model) {
	case "wan3.0-video", "wan3.0-video-prime":
		return true
	default:
		return false
	}
}

// validateAnimationDuration 校验视频生成时长：万相模型额外支持 duration=-1 表示智能时长
func validateAnimationDuration(model string, duration int) error {
	if isWanAnimationModel(model) && duration == -1 {
		return nil
	}
	minDuration, maxDuration := 5, 15
	if isWanAnimationModel(model) {
		minDuration, maxDuration = 2, 30
	}
	if duration < minDuration || duration > maxDuration {
		return fmt.Errorf("duration must be between %d and %d seconds", minDuration, maxDuration)
	}
	return nil
}

func isAnimationProviderConfigured(model string) bool {
	if isWanAnimationModel(model) {
		return strings.TrimSpace(config.Cfg.Wan.APIKey) != "" &&
			(strings.TrimSpace(config.Cfg.Wan.APIBaseURL) != "" || strings.TrimSpace(config.Cfg.Wan.WorkspaceID) != "")
	}
	return strings.TrimSpace(config.Cfg.Ark.APIKey) != ""
}

func animationProviderName(model string) string {
	if isWanAnimationModel(model) {
		return "Wan"
	}
	return "Ark"
}

func anyAnimationProviderConfigured() bool {
	for _, model := range []string{
		"doubao-seedance-2-0-260128",
		"wan3.0-video",
	} {
		if isAnimationProviderConfigured(model) {
			return true
		}
	}
	return false
}

func (e *arkHTTPError) Error() string {
	return e.Message
}

type resolvedAnimationReferenceAsset struct {
	Source    string
	Name      string
	MimeType  string
	SignedURL string
}

func (h *AnimationHandler) findFileByKey(db *gorm.DB, key string) (*models.File, error) {
	normalizedKey := strings.TrimSpace(key)
	if normalizedKey == "" {
		return nil, fmt.Errorf("file key is required")
	}

	var file models.File
	if err := db.Where("`key` = ?", normalizedKey).First(&file).Error; err != nil {
		return nil, err
	}

	return &file, nil
}

func (h *AnimationHandler) buildSignedFileURL(file *models.File) (string, error) {
	ossClient := oss.GetClient()
	if ossClient == nil {
		return "", fmt.Errorf("file service not configured")
	}

	return ossClient.GetSignedURL(file.Key, 3600)
}

func (h *AnimationHandler) resolveReferenceURLs(db *gorm.DB, rawItems []string) ([]string, error) {
	urls := make([]string, 0, len(rawItems))
	for _, raw := range rawItems {
		normalized := strings.TrimSpace(raw)
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, "http://") || strings.HasPrefix(normalized, "https://") {
			urls = append(urls, normalized)
			continue
		}

		file, err := h.findFileByKey(db, normalized)
		if err != nil {
			return nil, err
		}
		signedURL, err := h.buildSignedFileURL(file)
		if err != nil {
			return nil, err
		}
		urls = append(urls, signedURL)
	}
	return urls, nil
}

func buildAssetNameFromSource(source string, fallback string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return fallback
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		if base := path.Base(parsed.Path); base != "." && base != "/" && base != "" {
			return base
		}
	}
	if base := path.Base(trimmed); base != "." && base != "/" && base != "" {
		return base
	}
	return fallback
}

func (h *AnimationHandler) resolveReferenceAssets(
	db *gorm.DB,
	rawItems []string,
	fallbackPrefix string,
) ([]resolvedAnimationReferenceAsset, error) {
	assets := make([]resolvedAnimationReferenceAsset, 0, len(rawItems))
	for idx, raw := range rawItems {
		normalized := strings.TrimSpace(raw)
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, "http://") || strings.HasPrefix(normalized, "https://") {
			assets = append(assets, resolvedAnimationReferenceAsset{
				Source:    normalized,
				Name:      buildAssetNameFromSource(normalized, fmt.Sprintf("%s %d", fallbackPrefix, idx+1)),
				SignedURL: normalized,
			})
			continue
		}

		file, err := h.findFileByKey(db, normalized)
		if err != nil {
			return nil, err
		}
		signedURL, err := h.buildSignedFileURL(file)
		if err != nil {
			return nil, err
		}
		assets = append(assets, resolvedAnimationReferenceAsset{
			Source:    normalized,
			Name:      strings.TrimSpace(file.OriginalName),
			MimeType:  strings.TrimSpace(file.MimeType),
			SignedURL: signedURL,
		})
	}
	return assets, nil
}

func (h *AnimationHandler) buildArkTaskURL(taskID string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(config.Cfg.Ark.APIBaseURL), "/")
	if taskID == "" {
		return baseURL + arkTaskCreatePath
	}
	return baseURL + arkTaskCreatePath + "/" + taskID
}

func (h *AnimationHandler) buildWanTaskURL(taskID string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.Cfg.Wan.APIBaseURL), "/")
	if baseURL == "" {
		workspaceID := strings.TrimSpace(config.Cfg.Wan.WorkspaceID)
		region := strings.TrimSpace(config.Cfg.Wan.Region)
		if region == "" {
			region = "cn-beijing"
		}
		if workspaceID == "" {
			return "", fmt.Errorf("Wan video generation service is not configured")
		}
		baseURL = fmt.Sprintf("https://%s.%s.maas.aliyuncs.com", url.PathEscape(workspaceID), url.PathEscape(region))
	}

	if taskID == "" {
		return baseURL + wanTaskCreatePath, nil
	}
	return baseURL + wanTaskQueryPath + "/" + url.PathEscape(taskID), nil
}

func (h *AnimationHandler) buildArkRequest(ctx context.Context, method string, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(config.Cfg.Ark.APIKey))
	return req, nil
}

func (h *AnimationHandler) buildWanRequest(ctx context.Context, method string, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(config.Cfg.Wan.APIKey))
	if method == http.MethodPost {
		req.Header[HeaderDashScopeAsync] = []string{"enable"}
	}
	return req, nil
}

func (h *AnimationHandler) parseArkErrorResponse(resp *http.Response) string {
	raw, _ := io.ReadAll(resp.Body)
	message := strings.TrimSpace(string(raw))
	if message == "" {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		for _, key := range []string{"message", "error", "detail"} {
			if next := stringifyAny(payload[key]); next != "" {
				return next
			}
		}
	}

	return message
}

func (h *AnimationHandler) fetchArkTaskStatus(ctx context.Context, client *http.Client, taskID string) (*arkTaskStatusResponse, error) {
	endpoint := h.buildArkTaskURL(taskID)
	req, err := h.buildArkRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		message := h.parseArkErrorResponse(resp)
		resp.Body.Close()
		if message == "" {
			message = "Failed to query Ark task status"
		}
		return nil, &arkHTTPError{StatusCode: resp.StatusCode, Message: message}
	}

	var payload arkTaskStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to decode Ark task status: %w", err)
	}
	resp.Body.Close()

	return &payload, nil
}

func (h *AnimationHandler) fetchWanTaskStatus(ctx context.Context, client *http.Client, taskID string) (*arkTaskStatusResponse, error) {
	endpoint, err := h.buildWanTaskURL(taskID)
	if err != nil {
		return nil, err
	}
	req, err := h.buildWanRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		message := h.parseArkErrorResponse(resp)
		resp.Body.Close()
		if message == "" {
			message = "Failed to query Wan task status"
		}
		return nil, &arkHTTPError{StatusCode: resp.StatusCode, Message: message}
	}

	var payload wanTaskStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to decode Wan task status: %w", err)
	}
	resp.Body.Close()

	return &arkTaskStatusResponse{
		ID:      strings.TrimSpace(payload.Output.TaskID),
		Model:   strings.TrimSpace(payload.Output.TaskID),
		Status:  strings.TrimSpace(payload.Output.TaskStatus),
		Error:   firstNonEmpty(payload.Output.Code, payload.Code),
		Message: firstNonEmpty(payload.Output.Message, payload.Message),
		Content: struct {
			VideoURL string `json:"video_url"`
		}{VideoURL: strings.TrimSpace(payload.Output.VideoURL)},
	}, nil
}

func (h *AnimationHandler) fetchRemoteTaskStatus(ctx context.Context, client *http.Client, task *models.SceneAnimationGenerationTask) (*arkTaskStatusResponse, error) {
	if isWanAnimationModel(task.Model) {
		remoteStatus, err := h.fetchWanTaskStatus(ctx, client, task.ArkTaskID)
		if remoteStatus != nil {
			remoteStatus.Model = strings.TrimSpace(task.Model)
		}
		return remoteStatus, err
	}
	return h.fetchArkTaskStatus(ctx, client, task.ArkTaskID)
}

func (h *AnimationHandler) createWanGenerationTask(
	ctx context.Context,
	client *http.Client,
	task *models.SceneAnimationGenerationTask,
	prompt string,
	referenceImageAssets []resolvedAnimationReferenceAsset,
	referenceVideoAssets []resolvedAnimationReferenceAsset,
	referenceAudioAssets []resolvedAnimationReferenceAsset,
) (string, error) {
	requestBody := wanGenerationRequest{
		Model: task.Model,
	}
	requestBody.Input.Prompt = prompt
	requestBody.Input.Media = make([]wanMediaItem, 0,
		len(referenceImageAssets)+len(referenceVideoAssets)+len(referenceAudioAssets))
	for _, item := range referenceImageAssets {
		requestBody.Input.Media = append(requestBody.Input.Media, wanMediaItem{
			Type: "reference_image",
			URL:  item.SignedURL,
		})
	}
	for _, item := range referenceVideoAssets {
		requestBody.Input.Media = append(requestBody.Input.Media, wanMediaItem{
			Type: "reference_video",
			URL:  item.SignedURL,
		})
	}
	for _, item := range referenceAudioAssets {
		requestBody.Input.Media = append(requestBody.Input.Media, wanMediaItem{
			Type: "reference_audio",
			URL:  item.SignedURL,
		})
	}
	requestBody.Parameters.Resolution = "720P"
	requestBody.Parameters.Ratio = task.Ratio
	requestBody.Parameters.Duration = task.Duration
	requestBody.Parameters.Audio = true
	requestBody.Parameters.PromptExtend = true
	requestBody.Parameters.Watermark = false

	encodedBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("Failed to encode Wan request")
	}
	endpoint, err := h.buildWanTaskURL("")
	if err != nil {
		return "", err
	}
	req, err := h.buildWanRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(encodedBody))
	if err != nil {
		return "", fmt.Errorf("Failed to create Wan request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Wan video generation service is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		message := h.parseArkErrorResponse(resp)
		if message == "" {
			message = "Wan video generation request failed"
		}
		return "", fmt.Errorf("%s", message)
	}

	var payload wanTaskCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("Failed to decode Wan task response")
	}
	taskID := strings.TrimSpace(payload.Output.TaskID)
	if taskID == "" {
		message := firstNonEmpty(payload.Output.Message, payload.Message, payload.Output.Code, payload.Code, "Wan task ID is empty")
		return "", fmt.Errorf("%s", message)
	}
	return taskID, nil
}

func (h *AnimationHandler) createArkGenerationTask(
	ctx context.Context,
	client *http.Client,
	task *models.SceneAnimationGenerationTask,
	prompt string,
	referenceImageAssets []resolvedAnimationReferenceAsset,
	referenceVideoAssets []resolvedAnimationReferenceAsset,
	referenceAudioAssets []resolvedAnimationReferenceAsset,
) (string, error) {
	content := []arkGenerationContentItem{{
		Type: "text",
		Text: prompt,
	}}
	for _, item := range referenceImageAssets {
		content = append(content, arkGenerationContentItem{
			Type:     "image_url",
			ImageURL: &arkMediaURL{URL: item.SignedURL},
			Role:     "reference_image",
		})
	}
	for _, item := range referenceVideoAssets {
		content = append(content, arkGenerationContentItem{
			Type:     "video_url",
			VideoURL: &arkMediaURL{URL: item.SignedURL},
			Role:     "reference_video",
		})
	}
	for _, item := range referenceAudioAssets {
		content = append(content, arkGenerationContentItem{
			Type:     "audio_url",
			AudioURL: &arkMediaURL{URL: item.SignedURL},
			Role:     "reference_audio",
		})
	}

	encodedBody, err := json.Marshal(arkGenerationRequest{
		Model:         task.Model,
		Content:       content,
		GenerateAudio: true,
		Ratio:         task.Ratio,
		Duration:      task.Duration,
		Watermark:     false,
	})
	if err != nil {
		return "", fmt.Errorf("Failed to encode Ark request")
	}
	req, err := h.buildArkRequest(ctx, http.MethodPost, h.buildArkTaskURL(""), bytes.NewReader(encodedBody))
	if err != nil {
		return "", fmt.Errorf("Failed to create Ark request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Ark video generation service is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		message := h.parseArkErrorResponse(resp)
		if message == "" {
			message = "Ark video generation request failed"
		}
		return "", fmt.Errorf("%s", message)
	}

	var payload arkTaskCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("Failed to decode Ark task response")
	}
	taskID := strings.TrimSpace(payload.ID)
	if taskID == "" {
		message := firstNonEmpty(payload.Message, stringifyAny(payload.Error), "Ark task ID is empty")
		return "", fmt.Errorf("%s", message)
	}
	return taskID, nil
}

func (h *AnimationHandler) createRemoteGenerationTask(
	ctx context.Context,
	task *models.SceneAnimationGenerationTask,
	prompt string,
	referenceImageAssets []resolvedAnimationReferenceAsset,
	referenceVideoAssets []resolvedAnimationReferenceAsset,
	referenceAudioAssets []resolvedAnimationReferenceAsset,
) (string, error) {
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	if isWanAnimationModel(task.Model) {
		return h.createWanGenerationTask(ctx, httpClient, task, prompt, referenceImageAssets, referenceVideoAssets, referenceAudioAssets)
	}
	return h.createArkGenerationTask(ctx, httpClient, task, prompt, referenceImageAssets, referenceVideoAssets, referenceAudioAssets)
}

func stringifyAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(typed))
		}
		return strings.TrimSpace(string(encoded))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func pickVideoExtension(contentType string, sourceURL string) string {
	if exts, err := mime.ExtensionsByType(contentType); err == nil {
		for _, ext := range exts {
			if ext != "" {
				return ext
			}
		}
	}

	if parsed, err := url.Parse(sourceURL); err == nil {
		if ext := path.Ext(parsed.Path); ext != "" {
			return ext
		}
	}

	return ".mp4"
}

func encodeStringSlice(values []string) string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeStringSlice(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return []string{}
	}
	return values
}

func encodeReferenceAssets(values []models.AnimationTaskReferenceAsset) string {
	if len(values) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeReferenceAssets(raw string) []models.AnimationTaskReferenceAsset {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []models.AnimationTaskReferenceAsset{}
	}
	var values []models.AnimationTaskReferenceAsset
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return []models.AnimationTaskReferenceAsset{}
	}
	return values
}

func hydrateAnimationTask(task *models.SceneAnimationGenerationTask) {
	if task == nil {
		return
	}
	task.ReferenceImageAssets = decodeReferenceAssets(task.ReferenceImageAssetsJSON)
	task.ReferenceAudioAssets = decodeReferenceAssets(task.ReferenceAudioAssetsJSON)
	task.ReferenceVideoAssets = decodeReferenceAssets(task.ReferenceVideoAssetsJSON)

	if len(task.ReferenceImageAssets) > 0 {
		task.ReferenceImageKeys = make([]string, 0, len(task.ReferenceImageAssets))
		for _, item := range task.ReferenceImageAssets {
			if strings.TrimSpace(item.Source) != "" {
				task.ReferenceImageKeys = append(task.ReferenceImageKeys, item.Source)
			}
		}
	} else {
		task.ReferenceImageKeys = decodeStringSlice(task.ReferenceImageKeysJSON)
	}

	if len(task.ReferenceAudioAssets) > 0 {
		task.ReferenceAudioKeys = make([]string, 0, len(task.ReferenceAudioAssets))
		for _, item := range task.ReferenceAudioAssets {
			if strings.TrimSpace(item.Source) != "" {
				task.ReferenceAudioKeys = append(task.ReferenceAudioKeys, item.Source)
			}
		}
	} else {
		task.ReferenceAudioKeys = decodeStringSlice(task.ReferenceAudioKeysJSON)
	}

	if len(task.ReferenceVideoAssets) > 0 {
		task.ReferenceVideoKeys = make([]string, 0, len(task.ReferenceVideoAssets))
		for _, item := range task.ReferenceVideoAssets {
			if strings.TrimSpace(item.Source) != "" {
				task.ReferenceVideoKeys = append(task.ReferenceVideoKeys, item.Source)
			}
		}
	} else {
		task.ReferenceVideoKeys = decodeStringSlice(task.ReferenceVideoKeysJSON)
	}
}

func hydrateAnimationTasks(tasks []models.SceneAnimationGenerationTask) {
	for idx := range tasks {
		hydrateAnimationTask(&tasks[idx])
	}
}

func mapArkStatusToAnimationTaskStatus(status string) models.AnimationTaskStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded":
		return models.AnimationTaskStatusSucceeded
	case "failed", "canceled", "cancelled", "expired", "unknown":
		return models.AnimationTaskStatusFailed
	case "submitted", "queued", "pending":
		return models.AnimationTaskStatusPending
	default:
		return models.AnimationTaskStatusProcessing
	}
}

func isTerminalArkPollError(err error) bool {
	var arkErr *arkHTTPError
	return errors.As(err, &arkErr) && arkErr.StatusCode >= http.StatusBadRequest && arkErr.StatusCode < http.StatusInternalServerError
}

func recordArkPollError(db *gorm.DB, task *models.SceneAnimationGenerationTask, err error) error {
	if task == nil || err == nil {
		return nil
	}

	now := time.Now()
	message := firstNonEmpty(err.Error(), "Failed to query "+animationProviderName(task.Model)+" task status")
	updates := map[string]any{
		"last_polled_at": &now,
		"error_message":  message,
	}
	if isTerminalArkPollError(err) {
		updates["status"] = models.AnimationTaskStatusFailed
		updates["completed_at"] = &now
	}

	if updateErr := db.Model(task).Updates(updates).Error; updateErr != nil {
		return fmt.Errorf("failed to record generation task poll error")
	}
	if reloadErr := db.First(task, task.ID).Error; reloadErr != nil {
		return fmt.Errorf("failed to reload generation task")
	}
	return nil
}

func (h *AnimationHandler) getSceneAndAnimation(db *gorm.DB, sceneID string, animationID string) (*models.Scene, *models.SceneAnimation, error) {
	var scene models.Scene
	if err := db.First(&scene, sceneID).Error; err != nil {
		return nil, nil, fmt.Errorf("scene")
	}

	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationID, scene.ID).First(&animation).Error; err != nil {
		return &scene, nil, fmt.Errorf("animation")
	}

	return &scene, &animation, nil
}

// resolveBookArtStyle 读取场景所属作品的画风提示词（可选字段，读取失败时静默返回空）
func (h *AnimationHandler) resolveBookArtStyle(db *gorm.DB, scene *models.Scene) string {
	if scene == nil {
		return ""
	}
	var chapter models.Chapter
	if err := db.Select("book_id").First(&chapter, scene.ChapterID).Error; err != nil {
		return ""
	}
	var book models.Book
	if err := db.Select("art_style").First(&book, chapter.BookID).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(book.ArtStyle)
}

// buildFinalGenerationPrompt 组装最终提交给视频模型的提示词：开启追加且画风提示词非空时自动附加到用户提示词末尾
func buildFinalGenerationPrompt(text string, artStyle string, appendArtStyle bool) string {
	style := strings.TrimSpace(artStyle)
	if !appendArtStyle || style == "" {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n" + style
}

// resolveAppendArtStyle 解析请求中的“添加到提示词末尾”开关：省略字段时默认开启
func resolveAppendArtStyle(flag *bool) bool {
	return flag == nil || *flag
}

func (h *AnimationHandler) saveAnimationVersion(
	db *gorm.DB,
	task *models.SceneAnimationGenerationTask,
	scene *models.Scene,
	animation *models.SceneAnimation,
	currentUser *models.User,
	userID uint,
	sourceVideoURL string,
) error {
	if task.OutputVersion > 0 && strings.TrimSpace(task.ResultVideoUrl) != "" {
		return nil
	}

	httpClient := &http.Client{Timeout: animationVersionDownloadTimeout}
	downloadReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sourceVideoURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create generated video download request")
	}
	downloadResp, err := httpClient.Do(downloadReq)
	if err != nil {
		return fmt.Errorf("failed to download generated video")
	}
	defer downloadResp.Body.Close()

	if downloadResp.StatusCode >= http.StatusBadRequest {
		message := strings.TrimSpace(h.parseArkErrorResponse(downloadResp))
		if message == "" {
			message = "Failed to download generated video"
		}
		return errors.New(message)
	}

	videoBytes, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read generated video")
	}
	if len(videoBytes) == 0 {
		return fmt.Errorf("generated video is empty")
	}

	// 含软删除版本，避免版本号复用
	var maxVersion int
	db.Unscoped().Model(&models.SceneAnimationVersion{}).
		Where("scene_animation_id = ?", animation.ID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&maxVersion)

	newVersion := maxVersion + 1
	contentType := strings.TrimSpace(downloadResp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "video/mp4"
	}
	fileName := fmt.Sprintf(
		"scene-%d-animation-%d-v%d%s",
		scene.ID,
		animation.ID,
		newVersion,
		pickVideoExtension(contentType, sourceVideoURL),
	)
	objectKey, contentBytes, err := oss.GenerateKeyFromContent(bytes.NewReader(videoBytes), fileName)
	if err != nil {
		return fmt.Errorf("failed to process generated video")
	}

	ossClient := oss.GetClient()
	exists, err := ossClient.Exists(objectKey)
	if err != nil {
		return fmt.Errorf("failed to check generated video file")
	}
	if !exists {
		if err := ossClient.UploadBytes(objectKey, contentBytes, contentType); err != nil {
			return fmt.Errorf("failed to store generated video")
		}
	}

	fileRecord := models.File{
		Key:          objectKey,
		OriginalName: fileName,
		Size:         int64(len(contentBytes)),
		MimeType:     contentType,
		UploaderID:   currentUser.ID,
		Visibility:   models.FileVisibilityPrivate,
	}

	version := models.SceneAnimationVersion{
		SceneAnimationID: animation.ID,
		GenerationTaskID: &task.ID,
		VideoUrl:         objectKey,
		Version:          newVersion,
		CreatedBy:        userID,
	}

	now := time.Now()
	tx := db.Begin()
	var lockedTask models.SceneAnimationGenerationTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedTask, task.ID).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to lock generation task")
	}
	if lockedTask.OutputVersion > 0 && strings.TrimSpace(lockedTask.ResultVideoUrl) != "" {
		tx.Rollback()
		task.ResultVideoUrl = lockedTask.ResultVideoUrl
		task.OutputVersion = lockedTask.OutputVersion
		task.CompletedAt = lockedTask.CompletedAt
		task.ErrorMessage = lockedTask.ErrorMessage
		animation.AnimationUrl = lockedTask.ResultVideoUrl
		animation.AnimationVersion = lockedTask.OutputVersion
		return nil
	}
	if err := tx.Create(&fileRecord).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to save generated video file")
	}
	if err := tx.Create(&version).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to create generated video version")
	}
	if err := tx.Model(animation).Updates(map[string]any{
		"animation_url":     objectKey,
		"animation_version": newVersion,
	}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to update animation")
	}
	if err := tx.Model(task).Updates(map[string]any{
		"result_video_url": objectKey,
		"output_version":   newVersion,
		"completed_at":     &now,
		"error_message":    "",
	}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to update generation task result")
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to finalize generated video version")
	}

	task.ResultVideoUrl = objectKey
	task.OutputVersion = newVersion
	task.CompletedAt = &now
	task.ErrorMessage = ""
	animation.AnimationUrl = objectKey
	animation.AnimationVersion = newVersion
	return nil
}

func (h *AnimationHandler) applyRemoteGenerationTaskStatus(
	db *gorm.DB,
	task *models.SceneAnimationGenerationTask,
	remoteStatus *arkTaskStatusResponse,
) error {
	if task == nil || remoteStatus == nil {
		return nil
	}

	now := time.Now()
	nextStatus := mapArkStatusToAnimationTaskStatus(remoteStatus.Status)
	updates := map[string]any{
		"status":         nextStatus,
		"last_polled_at": &now,
	}
	if actualModel := strings.TrimSpace(remoteStatus.Model); actualModel != "" {
		updates["actual_model"] = actualModel
	}

	switch nextStatus {
	case models.AnimationTaskStatusFailed:
		message := firstNonEmpty(
			remoteStatus.Message,
			stringifyAny(remoteStatus.LastError),
			stringifyAny(remoteStatus.Error),
			animationProviderName(task.Model)+" video generation failed",
		)
		updates["error_message"] = message
		updates["completed_at"] = &now
	case models.AnimationTaskStatusSucceeded:
		if strings.TrimSpace(remoteStatus.Content.VideoURL) == "" {
			updates["status"] = models.AnimationTaskStatusFailed
			updates["error_message"] = animationProviderName(task.Model) + " task succeeded but returned empty video URL"
			updates["completed_at"] = &now
			break
		}

		var scene models.Scene
		if err := db.First(&scene, task.SceneID).Error; err != nil {
			return fmt.Errorf("failed to load generation task scene")
		}

		var animation models.SceneAnimation
		if err := db.Where("id = ? AND scene_id = ?", task.SceneAnimationID, task.SceneID).First(&animation).Error; err != nil {
			return fmt.Errorf("failed to load generation task animation")
		}

		var creator models.User
		if err := db.First(&creator, task.CreatedBy).Error; err != nil {
			return fmt.Errorf("failed to load generation task creator")
		}

		if err := h.saveAnimationVersion(db, task, &scene, &animation, &creator, task.CreatedBy, remoteStatus.Content.VideoURL); err != nil {
			retryUpdates := map[string]any{
				"status":         models.AnimationTaskStatusProcessing,
				"last_polled_at": &now,
				"error_message":  err.Error(),
			}
			if actualModel := strings.TrimSpace(remoteStatus.Model); actualModel != "" {
				retryUpdates["actual_model"] = actualModel
			}
			if err := db.Model(task).Updates(retryUpdates).Error; err != nil {
				return fmt.Errorf("failed to update generation task retry state")
			}
			task.Status = models.AnimationTaskStatusProcessing
			task.LastPolledAt = &now
			task.ErrorMessage = err.Error()
			if actualModel := strings.TrimSpace(remoteStatus.Model); actualModel != "" {
				task.ActualModel = actualModel
			}
			return nil
		}

		updates["status"] = models.AnimationTaskStatusSucceeded
		updates["error_message"] = ""
		updates["completed_at"] = task.CompletedAt
	default:
		updates["error_message"] = ""
	}

	if err := db.Model(task).Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update generation task")
	}

	if err := db.First(task, task.ID).Error; err != nil {
		return fmt.Errorf("failed to reload generation task")
	}

	return nil
}

func (h *AnimationHandler) pollGenerationTaskOnce(
	ctx context.Context,
	db *gorm.DB,
	httpClient *http.Client,
	task *models.SceneAnimationGenerationTask,
) error {
	if task == nil {
		return nil
	}
	if task.Status == models.AnimationTaskStatusSucceeded || task.Status == models.AnimationTaskStatusFailed {
		return nil
	}
	if strings.TrimSpace(task.ArkTaskID) == "" {
		return nil
	}

	remoteStatus, err := h.fetchRemoteTaskStatus(ctx, httpClient, task)
	if err != nil {
		if recordErr := recordArkPollError(db, task, err); recordErr != nil {
			return recordErr
		}
		return nil
	}

	return h.applyRemoteGenerationTaskStatus(db, task, remoteStatus)
}

func (h *AnimationHandler) pollPendingGenerationTasksOnce(ctx context.Context) {
	if !animationTaskPollerRunning.CompareAndSwap(false, true) {
		return
	}
	defer animationTaskPollerRunning.Store(false)

	db := database.GetDB()
	if db == nil {
		return
	}

	httpClient := &http.Client{Timeout: animationTaskPollRequestTimeout}
	var lastTaskID uint

	for {
		var tasks []models.SceneAnimationGenerationTask
		query := db.Where("status IN ?", []models.AnimationTaskStatus{
			models.AnimationTaskStatusPending,
			models.AnimationTaskStatusProcessing,
		})
		if lastTaskID > 0 {
			query = query.Where("id > ?", lastTaskID)
		}
		if err := query.
			Order("id ASC").
			Order("updated_at ASC").
			Limit(animationTaskPollBatchSize).
			Find(&tasks).Error; err != nil {
			log.Printf("animation task poller: failed to query pending tasks: %v", err)
			return
		}

		if len(tasks) == 0 {
			return
		}

		for idx := range tasks {
			task := &tasks[idx]
			if err := h.pollGenerationTaskOnce(ctx, db, httpClient, task); err != nil {
				log.Printf("animation task poller: failed to poll task %d (ark=%s): %v", task.ID, task.ArkTaskID, err)
			}
		}
		lastTaskID = tasks[len(tasks)-1].ID

		if len(tasks) < animationTaskPollBatchSize {
			return
		}
	}
}

func (h *AnimationHandler) StartGenerationTaskPoller(ctx context.Context) {
	if !anyAnimationProviderConfigured() {
		log.Println("animation task poller disabled: video generation service is not configured")
		return
	}
	if oss.GetClient() == nil {
		log.Println("animation task poller disabled: file service is not configured")
		return
	}

	log.Printf("animation task poller started, interval=%s", animationTaskPollInterval)

	go func() {
		h.pollPendingGenerationTasksOnce(ctx)

		ticker := time.NewTicker(animationTaskPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("animation task poller stopped")
				return
			case <-ticker.C:
				h.pollPendingGenerationTasksOnce(ctx)
			}
		}
	}()
}

// buildSeedanceAnimationPromptOptimizeSystemPrompt Seedance 系列模型的一键优化系统提示词（电影分镜范式：
// 补强镜头语言、动作细节、情绪外化、声音设计、光影规则与素材约束）
func buildSeedanceAnimationPromptOptimizeSystemPrompt() string {
	return `你是 AI 视频生成提示词专家，同时也是电影级 AI 视频提示词导演和导演导师。用户会输入他们自己写的动画制作提示词（可能是图生视频、文生视频、多模态参考、编辑视频或延长视频），其中很多描述来自小白用户，不一定使用专业导演、分镜、摄影或声音术语。你的任务是：在内部诊断提示词问题，理解用户的朴素语言意图，推断并转写成对应的专业视听语言，再按照优秀电影分镜范式重写提示词，最终只输出完整的优化后提示词。你擅长把用户的动画制作提示词优化成稳定、可执行、镜头感强、素材约束清晰、剧情衔接自然的生成提示词，但不能机械套用样例的具体世界观，也不要自行添加整体风格、画质或约束词类收尾文案（画风提示词由系统在生成视频时自动追加到提示词末尾）。

【输出要求】
1. 只输出最终可直接提交给视频生成模型的完整优化后提示词。
2. 不要输出问题诊断、修改原因、进阶建议、标题、Markdown、代码块或寒暄。
3. 输出不仅要整理格式，还要补强镜头语言、动作细节、情绪外化、声音设计、光影规则、空间连续性和素材约束。
4. 保留用户原始创意意图、主体关系、场景重点、剧情事件、场景设定、参考素材编号（如图片1、图片2、音频1、视频1）、已有台词含义、镜头编号、运动方向、风格要求；只能在此基础上增强，不得重写成另一个剧情。
5. 如果原始提示词与场景/剧情上下文冲突，优先保留原始提示词；上下文只用于补全缺失的环境、运镜、对白、转场和氛围。
6. 如果原始提示词已经很详细，不要大幅压缩；应做结构化、补漏和消歧。只有当提示词明显重复、矛盾或过长失控时，才进行适度整理。
7. 上下文只能补全缺失信息，不得出现和原始提示词冲突的信息；如果上下文没有证据，不要臆造重大事件。
8. 不要臆造无法从原始提示词、参考素材或场景/剧情上下文确定的人物姓名、身份、服饰细节、客观外观或关键道具。
9. 用户使用非专业表达时，要在内部完成导演式理解和翻译，把“想表达什么”转成更准确的景别、机位、剪辑、叙事、声音和表演指令；但不要输出教学说明、术语解释或“我推测”。
10. 必须对背景音乐（如果原文或上下文中有音乐）、环境音、动作音效、人物对白、画外音和内心 OS 做导演级细化。用户只写“愤怒、开心、悲伤、紧张”等笼统情绪时，不得原样停留在抽象词层面；应结合剧情前因后果，合理补全声线、音色、语速、音量、气息、停顿、重音、句内情绪转折，以及音乐和音效在关键动作处的进入、变化与收束。
11. 上述声音补全属于允许的导演式推断：可以增强表演和声音层次、补充符合场景的拟音与节奏，但不得改变台词核心含义，不得凭空增加会改变剧情走向的新对白、新人物、新事件或关键道具。

【任务类型识别】
首先判断用户任务属于哪种类型，使用对应句式框架：

1. 多模态参考：从素材提取元素生成全新视频。
   推荐句式：参考<图片N>中的<主体N>，生成...
   图片参考素材中的人物、物体、场景要尽量绑定清楚，必要时先定义主体。

2. 编辑视频：在原视频基础上局部或全局修改。
   推荐句式：严格编辑<视频N>，将其中的<原特征>修改为<新特征>。
   注意：编辑视频任务直接用<视频N>指代，不要加“参考”二字，否则容易被误判为参考任务。

3. 延长视频：在时间维度延续原视频。
   推荐句式：向前/向后延长<视频N>，生成...
   注意：延长视频任务直接用<视频N>指代，不要加“参考”二字。

4. 文生视频：纯文字描述生成视频。
   使用进阶公式完整描述：精准主体 + 动作细节 + 场景环境 + 光影色调 + 镜头运镜。

【参考素材绝对优先规则】
1. 如果原始提示词里引用图片、音频或视频，必须严格遵循引用素材，不得忽略、改名、调换顺序或凭空重设外观。
2. 手写引用如“图1”“图2”“音频1”“视频1”“@[IMG_GEN_...]”“@[02905...]”都视为真实有效素材，必须原样保留或用等价中文明确绑定。
3. 如果请求中提供了图片上下文，把它们按顺序视为“图片1、图片2、图片3...”，并与原始提示词中的图片引用保持一致；如果无法完全确认对应关系，必须保守描述为“图片N中的主体/场景/道具”。
4. 图片可用于理解人物外观、服装、道具、场景结构、光线、构图、空间关系，但不要编造图片外没有依据的客观外观。
5. 音频引用只用于音色、语气、节奏、情绪参考；不得改写为不存在的音乐或 BGM。
6. 视频引用如果被描述为延续、尾帧、回忆、编辑或参考，必须保持任务类型：延续就延续，编辑就编辑，回忆就作为蒙太奇/插叙画面，不要误写成普通图片参考。
7. 示例：如果用户写“人物1在场景1下进行了跳跃”，优化重点是跳跃动作的准备、起跳、滞空、落地、重心、衣料/发丝/尘土、镜头跟随、音效和情绪，而不是凭空更改人物外观或场景结构。

【小白描述的导演术语转写规则】
1. 先理解用户朴素描述背后的视听意图，再选择合适专业表达。可以把“镜头看着他”“拍脸”“拉近看看表情”转写为近景、特写、缓慢推近、浅景深、焦点落在面部；把“从远处看整个地方”转写为远景/全景/建立镜头；把“跟着他走”转写为平稳跟拍或手持跟拍。
2. 用户描述两个人对话、互相看、一个说完另一个反应时，可根据空间关系转写为正反打、内反打、外反打、过肩镜头、反应镜头或主观视角；不要机械堆叠术语，选择最能表达关系的一种或两种即可。
3. 用户写“旁边有人说”“画面外传来声音”“听到某人说话”时，优先判断为画外音或画外对白；写“他心里想”“内心声音”“脑子里响起”时，转写为内心独白；写“有旁白介绍/讲述”时，转写为旁白/解说。台词或旁白内容必须用{}包裹，音效用<>包裹。
4. 用户按事件自然发生顺序描述时，保持平铺直叙/线性叙事；用户写“闪回、回忆、脑海里出现、过去的画面”时，转写为闪回或回忆蒙太奇；用户写“很多片段快速出现、一幕幕掠过、几个画面交替”时，转写为蒙太奇剪辑、交叉剪辑或插叙，但不得改写剧情事实。
5. 用户写“突然切到、画面一转、下一秒、镜头跳到”时，按意图转写为硬切、匹配剪辑、动作接剪、淡入淡出或转场；如果只是普通连续动作，不要强行加花哨转场。
6. 用户写“让观众看到他看到的东西”“他的视角”“像我在现场看”时，可转写为主观镜头、过肩视角、第一视角或沉浸式手持镜头；用户写“别人不知道但观众知道”时，可转写为悬念铺垫、信息差构图或反应镜头。
7. 用户写“气氛很尴尬/压抑/紧张/感动/热闹”等抽象感受时，不要只保留情绪词，要转成镜头距离、表演节奏、停顿、环境声、光影色调、构图留白和动作细节。
8. 术语转写必须服务生成稳定性和用户意图：可以补专业术语，但不要把小白提示词改成炫技分镜；无法确定是哪种专业手法时，使用保守、通用、可执行的镜头描述。

【进阶公式（核心框架）】
完整提示词应尽量包含：精准主体 + 动作细节 + 场景环境 + 光影色调 + 镜头运镜。原文已有的视觉风格、画质与约束条件描述原样保留，不要自行新增。

1. 精准主体定义
- 使用 2-3 个清晰稳定的静态特征定义主体，例如服饰、发型、外观、配饰、体态、物种等。
- 推荐句式：将<图片/视频N>中的[主体核心特征]定义为<主体N>。
- 多主体时分别定义主体1、主体2等标签，后续描述持续使用同一标签，不要省略或混用指代。
- 未定义主体的简单参考场景，可以使用“<主体N>@<图片N>”绑定素材。
- 如果无法确定具体静态特征，不要编造；改用“图片N中的主要人物/主要主体”或“主体1@图片N”。

2. 分镜时序与镜头分解（复杂场景必用）
- 将复杂内容拆分为“镜头1 / 镜头2 / 镜头3”，按时间顺序推进；每个镜头尽量包含：景别、机位、焦段/镜头感、构图、主体位置、动作细节、表情情绪、运镜、光影、声音/台词。
- 不要指定精确秒数，如“0-3秒”“第5秒”，模型支持不稳定。
- 单个提示词不宜写成完整长剧本；应浓缩为清晰可执行的视频镜头描述。

3. 构图与空间关系
- 写清画面左右/前后/远近关系、主体占画面比例、前景遮挡、背景虚化、人物是否自然裁切、角色之间的距离和遮挡关系。
- 如果用户提到延续上一视频、尾帧、前后镜头或位置锁定，必须明确“人物位置、朝向、道具状态、镜头方向与上一段保持连续”，并避免出现多余人物或错误场景。

4. 动作描述
- 动作要具体到身体部位（手、腿、头部、肩背、眼神、呼吸等）+ 幅度/速度/力度，并补强动作过渡和衔接，让画面自然连贯。
- 优先使用缓慢、轻柔、连贯的小动作，避免狂奔、大跳、剧烈翻滚、夸张爆炸等高爆发动作，除非用户明确要求。
- 情绪要用具体身体细节表达，避免只写“很悲伤”“非常愤怒”“很开心”“很紧张”等抽象词。
  - 悲伤可改为：低头、肩膀微微颤抖、眼眶泛红、手指无意识攥紧衣角。
  - 喜悦可改为：嘴角抑制不住地上扬、眉眼舒展、脚步变得轻快。
  - 紧张可改为：频繁看手表、手指不停敲击桌面、呼吸急促、眼神闪躲。
  - 愤怒可改为：双拳紧握、下颌线紧绷、胸口剧烈起伏、眼神锐利。
- 可补充手指收紧、肩膀下沉、下颌绷紧、眼神停顿、呼吸变浅、衣摆被风吹动、道具轻微晃动、机械关节发出低声摩擦等细节化动作。

5. 运镜写法
- 直接使用标准运镜术语：中景、特写、近景、全景、缓慢推镜、缓慢拉远、平稳横移、固定镜头、跟拍、俯拍、低机位等。
- 一个镜头只指定 1 种主要运镜方式，不要同时要求推、拉、摇、移等多种复杂运镜；也不要叠加互相冲突的运镜。
- 必要时可补充：手持呼吸感、缓慢推近、平稳横移、低机位仰拍、过肩镜头、长焦压缩、浅景深、虚焦到对焦、蒙太奇转场等。
- 若原提示词已有运镜，保留其核心方向并规范成标准术语。

6. 场景环境与光影色调
- 场景环境要写清地点、空间层次、关键背景元素、天气或时间氛围。
- 光影必须来自场景和参考素材的合理光源，不随意添加摄影棚补光。可使用：自然逆光、夕阳侧逆光、冷蓝阴影、霓虹频闪、体积雾、玻璃反射、金属反光、浅景深、电影级调色，以及暖黄色灯光、冷蓝色调、柔和逆光、自然日光、阴天漫反射等。
- 不要自行添加整体画风、风格锁定或画质描述；用户原文明确要求风格转换（如“整体转为动漫风”）时按用户要求处理，用户没有要求时不要添加风格转换描述。

7. 声音设计与声音表演（重点优化项）
- 声音不是末尾附加说明，必须与每个镜头的剧情节点、人物动作和情绪变化同步设计。先判断这一镜头需要背景音乐、环境底噪、关键拟音、对白/画外音/旁白/内心 OS 中的哪些层次，再把必要声音写到对应动作或台词附近，明确先后、叠加、打断、留白和收束关系。
- 背景音乐（如果有）：保留用户指定的音乐、曲风和音频引用，并结合剧情细化其情绪质地、核心乐器/音色、节奏密度、强弱与动态变化。至少说明音乐如何入场和随剧情发展，例如由远及近、渐强、渐弱、鼓点加密、低频下沉、在台词前压低、关键动作时达到高潮、反转处骤停、结尾只留环境声。不要只写“悲伤的音乐”“欢快的 BGM”“紧张的音乐”；应写成可感知、可执行的声音过程。原文及上下文均未要求或暗示音乐时，不要强行配乐，明确“有音效，无音乐”。
- 环境音与动作音效：根据地点、材质、距离和动作力度，补充少量但有叙事作用的声音，例如风雨、雷声、鸟鸣、室内底噪、远处人声、脚步、衣料摩擦、呼吸、敲门、门轴、杯碟、键盘或机械声。写清音源远近、轻重、疏密和空间感，并让关键音效准确落在动作、停顿、转折或切镜点上；可用突然静音或只保留单一环境声制造反差。不要无目的堆砌音效，也不要加入与场景无关的声音。
- 对白声音表演：保留台词原文和核心含义，用{}包裹；每段重要台词需按剧情选择并写清角色声线/音色、口音或语言（仅在原文或上下文有依据时）、音量、语速、吐字、气息、情绪强度与交流距离。不要只在台词前写“愤怒地/开心地/悲伤地说”，而要把抽象情绪转成可演绎指令，例如压低嗓音、短促急切、气息发颤、强忍哭腔、咬牙挤字、尾音拖长、近乎耳语、突然拔高或失控咆哮。
- 停顿、重音与句内转折：结合标点、语义和人物目的，为重要台词设计自然的停连、呼吸、重读和情绪递进；同一句台词前后态度发生变化时，要明确转折触发点和变化轨迹，例如前半句克制平稳，中间深吸气并短暂停顿，后半句语速加快、音量拔高；或先高昂坚定，听见消息后音乐骤停，声音转为颤抖低语。禁止整句只使用一个笼统情绪标签。
- 内心 OS / 内心独白：必须和人物直接说出口的对白、旁白、画外对白明确区分，标注为“内心 OS”或“内心独白”，台词同样用{}包裹。根据角色表面行为与真实心理的关系，细化更贴近内心的音色、距离感、气息、语速、停顿、潜台词和句内情绪转折；可以通过近耳感、轻微混响或与现场声的层次差表现主观心理空间，但不要写成角色嘴部发声。若角色表面平静而内心崩溃、怀疑或狂喜，要同时写清外在表演的克制与 OS 声音的反差。
- 画外音、旁白与多角色：画外对白要交代声音来自画面外及大致方位/距离；旁白要确定讲述质感而非套用角色口吻；多角色对话分别固定各自声线、说话节奏与情绪变化，结合抢话、打断、沉默或回应时机保持可辨识性。不得把不同角色台词和音色混在一起。
- 声画协同：音乐、音效、呼吸、沉默和台词必须服务同一情绪曲线，并与拍桌、开门、回头、发现真相、情绪爆发、切镜等可见节点对齐。关键台词需要听清时，让音乐主动压低或留白；高潮后可让音乐延续片刻再戛然而止，只留下风声、呼吸或其他具有余韵的环境声。

【声音优化范例（只学习抽象方法，不得照抄）】
以下范例已经按特殊字符规范整理，用于展示声音描述应达到的具体程度。只能迁移“音乐动态 + 环境/动作音效 + 声线与节奏 + 停顿/转折 + 声画同步”的方法，不得把范例中的人物、口音、台词、道具、场景或剧情强行加入用户提示词；实际输出仍以用户原始意图和参考素材为准。

范例1｜史诗战争·情绪逐级爆发：
（史诗般宏大低沉的铜管乐开场。）<近处狂风呼啸，远方传来低沉雷声。>男主使用中年、低沉、极度沙哑沧桑的声线，先以缓慢克制却充满力量的语气说道：{在无尽的黑暗中，我们已经失去了一切。}（铜管乐随台词逐渐压低，密集鼓点开始加入。）男主的音量和语势逐步抬升，语气转为坚定且极具煽动性：{但只要这颗心脏还在跳动……}<一道巨大的雷鸣在停顿处劈下。>男主紧接着用尽全力咆哮，最后几个字重读并拉满爆发力：{我们就将战至最后一刻！！！}（史诗交响乐在咆哮后冲至最高潮，短暂延续后戛然而止。）结尾只剩<空旷的风声>。

范例2｜四川茶馆·环境声衬托动作反转：
<嘈杂的四川茶馆里，清脆密集的搓麻将声与热闹交谈声交叠。>女主使用鲜明川渝口音，先发出<极其不耐烦的啧声>，随后以大音量、急促而泼辣的语气抱怨：{哎呀！你莫扯把子了嘛！一天到晚神戳戳的！}<她突然重重拍桌，响亮的拍桌声落下，麻将声和交谈声瞬间停止。>短暂寂静后，女主语调猛然拔高，情绪由不耐烦转为兴奋，连珠炮般大吼：{胡了！清一色！搞快点搞快点，给钱给钱！}

范例3｜深夜敲门·无音乐的留白与空间感：
深夜室内保持死寂，无音乐。<门外传来三声清脆、急促且间隔很短的敲门声。>男主刚被吵醒，声音慵懒含混、气息松散，带着明显不耐烦拖长尾音：{谁啊，大半夜的。}<沉重的门把手缓慢转动，旧门轴发出低哑摩擦声。>随后<缓慢的木地板脚步声>由近及远响起，以单一环境拟音维持悬念。

范例4｜战争反转·音乐骤停制造情绪坠落：
（激昂热血的交响乐持续铺底，铜管与鼓点不断推高气势。）女主用充满斗志的高昂声线、清晰有力的重音大喊：{全军出击！}（最后一个字落下时交响乐骤然切断。）环境瞬间陷入死寂，只剩<空旷而冰冷的风声>。女主听到战报后呼吸停滞，声线由高昂骤然跌为微弱颤抖的低语，前半句难以置信，省略号处停顿，后半句气息发虚：{怎么会……全军覆没了……}

范例5｜情侣信用卡·同一角色句间情绪急转：
（轻快明亮的尤克里里音乐铺底。）男主先用极其温柔、宠溺的声线放慢语速，尾音柔和上扬：{亲爱的，想买什么我都给你买。}（尤克里里在句末突然停止。）<男主深吸一大口气。>短暂压抑后，他的语调瞬间切换为极度暴躁，咬紧牙关挤出前半句，再将后半句拔高成失控怒吼：{但你能不能别刷我的信用卡了！！！}

范例6｜新闻整活·专业口吻被喜剧停顿打破：
（舒缓克制的新闻垫乐轻声铺底。）女主先以专业、平稳、吐字清晰的播音腔说道：{据最新科学研究表明，经常熬夜的人……}（新闻垫乐在悬念处停止。）画面和声音短暂留白，女主突然<噗嗤笑出声>，声线切换为轻快俏皮、略带古怪的口吻：{会变得像我一样有黑眼圈！}<句尾同步出现短促滑稽的喜剧音效。>

范例7｜甲方改需求·克制、爆发与疲惫回落：
（轻柔治愈的纯音乐平稳铺底。）<规律的机械键盘敲击声持续响起。>男主先用专业、礼貌、平稳且略显克制的职场语气说道：{好的王总，这个方案我今天下班前一定给您发过去，没问题的。}<键盘声在通话结束后突然停止。>（治愈音乐随之戛然而止。）<男主深吸一大口气。>压抑的情绪短暂停顿后彻底爆发，他带着愤怒哭腔和失控的暴躁语调大喊，反复重读数量：{我改了八遍了！！八遍！！你连需求都说不明白！！}<他发出一阵神经质、带有嘲讽意味的冷笑。>随后音量骤降，以耗尽力气般疲惫、咬牙切齿的低语说道：{毁灭吧，赶紧的，累了。}最后以<沉重而巨大的摔键盘声>收尾，之后不再追加音乐。

8. 约束词
不要自行添加任何约束词类收尾文案（如“保持无字幕”“不要生成水印”“人物面部稳定不变形”“不穿模”“主体身份前后一致”“位置关系合理”等）；画风与技术约束由系统在生成视频时统一追加。用户原文中已写的约束词原样保留，只优化其所在语句的表达，不改变含义、不删除。

【特殊字符规范】
- 音乐使用中文全角括号：（背景中播放着快节奏的摇滚乐）。
- 音效使用尖括号：<远处传来狗叫声>。
- 台词使用花括号：{你好，世界}。
- 字幕使用中文方括号：【第一章：启程】。
- 台词语言需统一，避免中英文混用（专有名词除外）。
- 如果原提示词中有对白、画外音、旁白、内心 OS 或角色说话内容，必须用{}包裹，并在花括号外写明说话者、声音类型和表演方式；如果有音效，必须用<>包裹。
- 音乐的动态变化写在全角括号内，例如：（低沉弦乐在台词前渐弱，反转处骤停）。同一句台词的停顿、重音和情绪转折写在花括号外，不要把导演指令混入台词正文。
- 除非用户明确要求字幕，否则不要主动生成字幕内容。

【画风规则】
1. 不要自行添加任何整体画风、风格锁定（如“整体为…风格”“…质感”“对标某某作品/引擎”）或画质描述；画风提示词由系统在生成视频时自动追加到提示词末尾。
2. 用户原文中已有的风格、画质与对标描述原样保留，只优化其所在语句的表达，不改变含义、不删除。
3. 用户原文明确要求风格转换（如“整体转为动漫风”）时按用户要求处理；用户没有要求时，不要添加风格转换描述。

【上下文使用规则】
1. 当前章节及前后章节大纲用于理解剧情走向、人物目标、冲突关系和情绪背景。
2. 前序场景上下文只用于承接人物位置、道具状态、情绪余波、光线/空间连续性，不要主动把前序场景内容写进当前要生成的视频。
3. 当前场景是默认生成范围的核心，必须优先服务用户原始提示词。
4. 后续 1-4 个场景可能被用户合并成一段长镜头或一组连续分镜；如果原始提示词出现“接着、随后、然后、镜头2/3/4、长镜头、连续、一起生成”等连贯表达，可以把当前场景和后续场景合并理解并优化成连续镜头。
5. 如果原始提示词只描述当前场景，不要擅自把后续场景剧情塞进输出；后续场景只用于铺垫方向和避免断裂。
6. 如果上下文能判断人物正在延续上一场动作，要补充衔接。

【推荐输出结构】
你可以根据原始提示词复杂度选择结构，但复杂动画优先使用以下结构：

【参考素材约束】
说明图1/图2/音频1/视频1或@[...]分别用于什么，严格绑定人物、场景、道具、音色、延续/回忆关系。

镜头1：景别 + 机位 + 焦段/镜头感 + 构图 + 主体位置 + 动作细节 + 表情情绪 + 运镜 + 光影 + 声音/台词。
镜头2：延续上一镜头的空间关系，写清切镜点、主体动作变化、前后景和声音衔接。
镜头3：继续推进剧情，写清关键动作、情绪落点、转场或结尾画面。

【内部诊断清单（只用于思考，不要输出诊断）】
主体定义问题：
- 主体特征是否少于 2 个稳定特征。
- 多主体是否未分别定义标签。
- 后续主体指代是否不一致或省略。
- 编辑/延长任务是否误用“参考<视频N>”句式。

动作描述问题：
- 动作是否过于笼统，如只写“跑步”“开心”“悲伤”。
- 是否包含模型难以稳定生成的高爆发大动态动作。
- 情绪是否没有外化为具体身体动作。
- 是否缺少动作过渡衔接。

镜头/运镜/构图问题：
- 单个镜头是否指定了多种运镜方式。
- 复杂多场景是否未使用分镜时序。
- 是否指定了精确时间。
- 构图与空间关系是否写清。
- 连续性段落是否明确人物位置、朝向、道具状态、镜头方向与上一段保持连续。
- 是否把用户的小白描述转写成合适的景别、机位、反打、主观镜头、转场或剪辑手法。

参考素材遵循问题：
- 是否严格遵循图片/音频/视频参考，没有凭空改外观。
- 是否保留所有素材引用和原始剧情意图。
- 图片引用是否用于理解外观/道具/场景结构，而非编造额外细节。
- 视频/音频引用是否保持了原任务类型（延续/编辑/回忆/参考）。

结构完整性问题：
- 是否缺少场景/环境描述。
- 是否缺少光影色调描述。
- 是否避免自行添加整体风格、画质或约束词类收尾文案（画风由系统在生成视频时自动追加）。
- 是否逐镜检查了背景音乐（如果有）、环境音、动作音效、对白/旁白/画外音/内心 OS，而不是只写笼统的“悲伤/开心/愤怒”。
- 音乐是否写清曲风/音色、强弱、节奏以及入场、渐变、压低、高潮、骤停或收束，并和剧情节点同步。
- 音效是否具有明确音源、远近、轻重与触发动作，是否避免无意义堆砌。
- 每段重要台词和内心 OS 是否写清声线、语速、音量、气息、停顿、重音及句内情绪转折；不同角色和声音类型是否清楚可辨。
- 声音是否和动作、切镜、反转、爆发及沉默留白准确对齐，关键台词是否被音乐遮盖。
- 台词是否未用{}包裹。
- 音效是否未用<>包裹。

上下文使用问题：
- 是否引入了当前章节与前后章节、当前场景与前后场景的必要上下文。
- 上下文是否出现和原始提示词冲突的信息。

常见陷阱：
- 风格漂移风险：仅在用户明确要求风格转换时写明转换描述；不要自行添加整体风格描述。
- 人物 ID 漂移风险：人物参考不足时，不要过度编造。
- 参考人物超过 4 人时，提示词要尽量分清主体，不建议扩写成混乱群像。
- 提示词过于冗长时，应压缩成镜头级指令，而不是完整剧本。

【重写流程】
1. 读取原始提示词、当前场景摘要、剧情上下文（前后章节大纲、前后场景画面需求与台词/旁白）以及已上传/已引用的参考素材列表。
2. 判断任务类型：多模态参考 / 编辑视频 / 延长视频 / 文生视频。
3. 检查原文已有的风格与约束描述，优化表达但不改变含义，也不新增整体风格、画质或约束词内容。
4. 将用户的非专业描述转写为合适的导演、分镜、摄影、剪辑、叙事和声音术语，但只在最终提示词中自然呈现结果，不输出教学过程。
5. 内部逐项诊断问题（参考【内部诊断清单】）。
6. 严格遵循参考素材约束与上下文取舍规则。
7. 按进阶公式与推荐结构重构，保留用户原始创意意图，补强镜头、动作、情绪、光影、声音和连续性；为每个关键剧情节点设计声音进入、发展、转折和收束，并细化对白/内心 OS 的声线、停顿、重音与句内情绪变化。
8. 输出完整优化提示词；分镜用“镜头1：”“镜头2：”“镜头3：”标注。
9. 检查输出没有自行添加任何整体风格、画质或约束词类收尾文案；画风提示词由系统在生成视频时自动追加到提示词末尾。

【输出形态示例（仅参考结构，不要照抄内容）】
文生视频可输出：
镜头1：街巷侧面固定机位，男人从画面左侧入镜，双腿用力蹬地缓慢起跑，呼吸急促可见，肩膀微微前倾。
镜头2：镜头平稳跟拍，男人经过路边摊位时侧身避让，镜头给到他惊恐的面部特写，眼神闪躲、眉头紧锁。
镜头3：全景固定镜头，男人越过矮墙后消失在画面中，镜头缓慢拉远，定格在空荡寂静的街道。

图生视频可输出：
将图片1中[穿白色衬衫、长发]的女孩定义为主体1。
镜头1：咖啡店内，暖黄色灯光，中景固定镜头，主体1坐在靠窗位置，手轻轻端起咖啡杯，嘴角抑制不住地上扬，眉眼舒展，目光望向窗外。
镜头2：镜头缓慢推近至主体1面部近景，她轻轻抿一口咖啡，随后放下杯子，手指轻轻敲击桌面。`
}

// buildWanAnimationPromptOptimizeSystemPrompt 万相（Wan）3.0 系列模型的一键优化系统提示词，
// 移植自万相 3.0 官方提示词指南（wan3-pe skill）：本环节做"格式化整理"而非导演级扩写，
// 把用户文字请求连同素材引用梳理成【核心任务】【情节概要】【音频风格】【运镜与核心约束】【负面提示词】
// 五段结构化请求；生成阶段专用的表达增强交给万相模型自带的提示词增强（PE）环节完成。
func buildWanAnimationPromptOptimizeSystemPrompt() string {
	return `你是万相 3.0 视频生成提示词整理专家，严格按照万相官方提示词指南工作。用户会输入他们自己写的动画制作提示词（可能是纯文字描述、图片/音频/视频多模态参考、素材续写或素材入片），很多描述来自非专业用户。你的任务是：把用户的文字请求连同素材引用，梳理成一条干净、结构化、可直接提交给万相 3.0 的视频生成请求。成品固定为【核心任务】【情节概要】【音频风格】【运镜与核心约束】【负面提示词】五段：用户的核心意图照搬不动，素材参考统一落在【核心任务】，主体映射顺着情节节点自然带出。

本环节的性质是"格式化整理"，不是导演级扩写，边界如下：
- 只梳理用户已经写出的剧情、主体、台词与参考诉求；不替用户扩写剧情，不加入用户没提过的内容。
- 下游生成阶段自带提示词增强（PE）环节继续加工；本环节只把输入理清楚，生成阶段专用的表达交给后续环节完成。
- 素材全程只读：不改动原始素材，也不额外制作辅助素材。
- 画幅、总时长、分辨率、帧率等参数由系统页面或接口设置，只用来规划事件密度，不写进请求正文。
- 画风提示词由系统在生成视频时自动追加到提示词末尾；本环节不自行添加整体画风、风格锁定或画质描述，用户原文已有的风格描述原样保留、只做同义改写。

【输出要求】
1. 整条回复就是整理后的请求正文本身，直接以【核心任务】段落开头、以正文段落结束；不输出开场确认、结尾总结、问题诊断、整理说明、标题、Markdown、代码块或寒暄，也不在正文后追加"素材提示："、"补充说明："之类的内容。
2. 默认只交付一条最佳版本请求，不给对比版本。
3. 凡是能从原始提示词、参考素材和剧情上下文合理判断的，直接整理完成，不向用户反问；存疑但不影响核心结果的细节按用户原文保守处理。
4. 输出语言跟随原始提示词的主要语言；台词、歌词与画面文字保持原文。

【硬性约束】
1. 忠实原文：人物身份与数量、关键道具、场景、事件因果、空间关系、故事结局一律照用户原文，不增删、不篡改。
2. 事实与观察分离：身份、年龄、关系、事件与结局以用户文字为准；随请求提供的图片只补充能直接观察到的属性（五官、发型、服装、颜色、材质、空间布局、光线等），观察到的表象不升格成剧情事实；无从确认的品牌、职业、性格、道具功能一律不补写。用户只叫"人物"或"主体"时沿用这个中性称呼；用户已经给了角色名或身份，才跟着用那个称谓。
3. 主体基数匹配：一张单人设定图只对应一个人物；有多个单人候选时先按一人一图分配，有多个群体候选时先按一组一图分配；只有同一主体的多视角、或素材画面本身就含同一群体时，才合并参考。
4. 用户映射优先：用户点名指定的素材职责、主体名称与相互关系照搬不动；用户映射已覆盖某个职责时，不再为同一职责补进没点名的素材。
5. 素材逐份负责：每一份真正用到的素材，都写清采用它的什么、参考范围到哪里；一处只界定一个主体、道具或场景连同它的参考素材与采用范围，涉及多个主体就拆成多处分别绑定。同一主体有多份参考时，分别讲清每份补的是哪个视角或属性，并声明它们共同界定同一个实体、只生成一个实例。
6. 引用保真：原始提示词里的素材引用（如"图片1""图2""音频1""视频1""@[...]"）一律原样留住，不删除、不改号、不调换顺序，全程沿用同一套编号体系；用户没写素材引用时，不新增。
7. 不滥加约束：只整理用户提出的要求；用户没要的画质、水印、Logo、字幕或通用负向约束（如"人物面部稳定不变形""主体身份前后一致"）一律省略。
8. 台词逐字保留：用户给的台词、旁白、歌词逐字保留并绑定说话人与语言；用户只描述了说话意图、没给原文时，保留那段意图描述，不自己编对白。
9. 镜头时间戳：用户写了"镜头N xx-xx秒"这类时间戳，就按用户原文逐字带进【情节概要】对应节点；用户没写时间戳时各节点保持纯编号，不自己编造时间戳。

【素材职责分配】
每份被用到的素材只担一个明确职责：
- 图片：人物外貌与服装、商品结构与材质、道具、场景布局、光线，或某个画面节点（起始画面、过程关键画面、收尾画面）。
- 视频：动作、运镜、节奏、时间线，或作为完整画面区间进入成片（成片首段、中段、尾段）。
- 音频：说话人的音色与台词、环境声、音效或音乐，或作为完整声音区间进入成片。
映射冲突时按优先级从高到低取舍：用户明确指定 > 请求正文描述 > 素材自身内容 > 文件名与元数据 > 上传顺序。
用户明确映射已覆盖全部必需实体时，没被点名的其余素材默认不参与本次任务，不要为了用完素材而强行加入无关角色或场景。
图片素材可能随请求提供，按顺序视为"图片 1、图片 2..."的视觉上下文，只从中确认能直接观察到的特征；未随请求提供的图片、以及音频与视频素材，按用户原文与素材列表的职责如实整理，不假称已看过或听过，不补写只有看过或听过才能确认的细节。

【请求模板】
【核心任务】
生成一段<题材或类型>的<视频内容概述>，参考<视频N/图片N/音频N>中<主体或内容>的<形象/动作/场景/音色等采用内容>。全片采用<全局风格基调>。
素材参考在此集中写明；音色参考写为"<角色>的音色参考音频N"。

【情节概要】
1. <情节节点一：人物、动作、事件；用户台词逐字保留并绑定说话人>。
2. <情节节点二……>；使用音频N的音色说："<逐字原文>"。
<音色参考在对应说话节点写明。>
<用户为镜头写了时间戳时，对应节点以"镜头N xx-xx秒"开头，时间按用户原文逐字保留；用户未写时间戳时保持纯节点编号。>

【音频风格】
<对白语言与质感、现场音效、配乐的情绪与节奏走向。>
<用户未提及时保持一句简短概括或整段省略。>

【运镜与核心约束】
<镜头风格、运动方式、剪辑节奏，以及用户给出的硬性约束。>
<用户未提及时保持一句简短概括或整段省略。>

【负面提示词】
<逗号分隔的用户明确排除项。仅当用户明确要求排除某些内容时输出本段。>

段落取舍规则：【核心任务】和【情节概要】永远输出；【音频风格】【运镜与核心约束】看用户输入的详略来给，用户没提就写简短一句或整段省掉；【负面提示词】只有用户明确要求排除某些内容时才出现；简单任务可以只留核心两段。"全片采用<全局风格基调>"仅当用户原文明确给出风格基调时填写，没有就整句省略。用户原文里的"换成、替代"是在描述参考关系（比如让图片1的人物去做视频1里的动作），整理时改说成"谁在做什么"，把人物与动作、场景等参考之间的对应关系写清楚。

填写示例（简单任务，音频与运镜用户未提及）：
【核心任务】
生成一段修复旧木椅的手工记录视频，参考图片1中木工的形象与图片2中旧木椅的外观，参考视频1中的涂胶与压紧手部动作。全片采用平实自然的生活记录风格。

【情节概要】
1. 木工检查松动的椅背接缝。
2. 木工按视频1的手法涂抹木胶并把椅背压回原位固定。
3. 木工松开双手，椅背保持稳固。

【音频风格】
只保留木材摩擦与工具轻响等现场声，人物自然闭口。

【素材入片写法】
- 整图入片：用户要整幅图片当作画面进入成片时，图片的位置落进【核心任务】（起始画面、过程关键画面还是收尾画面，多张写清先后顺序）；图片与相邻内容怎么接、要留住的主体与空间关系，随【情节概要】对应节点写明；多宫格图片按阅读顺序逐格说清画面内容，指代主体时统一用整图编号。
- 视频区间入片：用户要完整视频或指定区间进入成片时，区间的位置落进【核心任务】（成片首段、中段还是尾段）；首段视频往后怎么接、尾段视频往前怎么衔、中段视频前后怎么连，随对应情节节点写明；区间内容照实际呈现的人物、动作、事件与声音如实概括进【情节概要】，区间与生成内容之间主体、空间与声音关系保持连续。
- 音频入片：用户要完整用上音频时，使用范围落进【核心任务】（贯穿全片，还是只占成片某个局部连续区间及其位置）；音频承载的人声、音乐、环境音写进【音频风格】，能确认的语言内容按说话人和逐字原文写进【情节概要】对应节点，音频与画面事件的对应关系随对应节点写明。

【台词与音色】
- 用户给的台词、旁白、歌词逐字留进【情节概要】对应节点，并写清说话人与语言。
- 音色参考在【核心任务】里声明"<角色>的音色参考音频N"，并在【情节概要】的说话节点写明"使用音频N的音色说：'逐字原文'"。
- 无对白任务写清人物自然闭口、声音从哪来、保留哪些环境声。
- 用户明确要画面显示字幕时，写清字幕内容与出现位置；没提字幕就不写。

【上下文使用规则】
1. 剧情上下文（前后章节大纲、前后场景信息）只用于理解原始提示词的指代、承接关系与情绪背景，不得把用户没写、上下文里才有的剧情事件、人物、道具或对白补进请求正文。
2. 当前场景是默认整理范围的核心；原始提示词只描述当前场景时，不要把后续场景内容塞进请求。
3. 用户原文提及延续上一段画面、尾帧、前后镜头或位置锁定时，才在对应节点写明"人物位置、朝向、道具状态与上一段保持连续"。

【交付前核对】
给出成品前逐条对照：
- 人物身份与数量、事件因果、道具归属、空间关系、故事结局都与用户原文对得上；每个事件、主体、台词都能在用户原文或其明确引用的素材里找到出处。
- 每份真正用到的素材都有唯一且明确的职责与采用范围；单人设定图只对应一个人物；多份素材共同界定同一主体时已明确声明。
- 素材参考集中落在【核心任务】，音色参考写在对应的说话节点；素材引用编号与用户原始请求保真一致；没假称看过没读到的素材，也没补写无法确认的细节。
- 台词、旁白、歌词逐字保留，说话人与语言归属无误；镜头时间戳按用户原文逐字带进【情节概要】。
- 画幅、总时长等接口参数没有写进正文；没有自行添加整体画风、画质或通用约束词；【负面提示词】只在用户明确要求排除内容时出现，内容全部来自用户的排除项。
- 成品以【核心任务】段落开头；【核心任务】【情节概要】齐全，【音频风格】【运镜与核心约束】按用户输入详略取舍；整条回复只有请求正文，直接以正文段落开始、也以正文段落结束。

【端到端示例（仅参考结构与整理方法，不要照抄内容）】
用户请求：以视频1为开头向后续写，图片1中的角色接过花束并微笑致谢，音频3是他的音色，说"非常感谢"。

整理后的请求正文：
【核心任务】
以视频1为成片首段、向后续写一条角色接过花束并致谢的视频；续写段落中角色的外貌参考图片1，该角色的音色参考音频3。全片保持视频1确立的画面基调。

【情节概要】
1. 视频1作为首段完整呈现原内容，画面、声音与事件顺序按原内容保留。
2. 图片1中的角色双手接过递来的花束，将花束收至胸前，面带微笑向对方点头致谢，使用音频3的音色说："非常感谢。"
3. 交接完成后，花束只在该角色手中。

【音频风格】
视频1首段保留原声；续写段落保留现场环境声与人声，配乐随致谢情绪自然收束。

【运镜与核心约束】
视频1到续写段落保持镜头风格连续，主体、场景方向与声音关系连贯，角色外貌与服装稳定。`
}

func truncatePromptText(text string, maxRunes int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return trimmed
	}
	return string(runes[:maxRunes]) + "..."
}

func formatChapterContextLine(chapter models.Chapter, current bool) string {
	label := "章节"
	if current {
		label = "当前章节"
	}
	parts := []string{fmt.Sprintf("%s %.2f：%s", label, chapter.Index, strings.TrimSpace(chapter.Title))}
	if synopsis := truncatePromptText(chapter.Synopsis, 600); synopsis != "" {
		parts = append(parts, "大纲："+synopsis)
	}
	return strings.Join(parts, "｜")
}

func formatSceneContextLine(scene models.Scene, current bool) string {
	label := "场景"
	if current {
		label = "当前场景"
	}
	parts := []string{fmt.Sprintf("%s %.2f", label, scene.Index)}
	if text := truncatePromptText(scene.Description, 500); text != "" {
		parts = append(parts, "画面需求："+text)
	}
	if text := truncatePromptText(scene.CameraMovement, 260); text != "" {
		parts = append(parts, "运镜："+text)
	}
	if text := truncatePromptText(scene.Dialogue, 360); text != "" {
		parts = append(parts, "台词/旁白："+text)
	}
	if text := truncatePromptText(scene.TransitionEffect, 200); text != "" {
		parts = append(parts, "转场："+text)
	}
	return strings.Join(parts, "｜")
}

func buildAnimationPromptOptimizeNarrativeContext(db *gorm.DB, scene models.Scene) string {
	var lines []string
	appendContextSection := func(title string, sectionLines []string) {
		if len(sectionLines) > 0 {
			lines = append(lines, title+"\n"+strings.Join(sectionLines, "\n"))
		}
	}

	var chapter models.Chapter
	if err := db.First(&chapter, scene.ChapterID).Error; err == nil {
		var book models.Book
		if err := db.First(&book, chapter.BookID).Error; err == nil {
			bookLines := []string{}
			if title := strings.TrimSpace(book.Title); title != "" {
				bookLines = append(bookLines, "作品："+title)
			}
			if desc := truncatePromptText(book.Description, 500); desc != "" {
				bookLines = append(bookLines, "简介："+desc)
			}
			if outline := truncatePromptText(book.Outline, 1200); outline != "" {
				bookLines = append(bookLines, "全书大纲摘要："+outline)
			}
			if len(bookLines) > 0 {
				lines = append(lines, "【作品信息】\n"+strings.Join(bookLines, "\n"))
			}
		}

		var chapters []models.Chapter
		if err := db.Where("book_id = ?", chapter.BookID).Order("`index` asc, id asc").Find(&chapters).Error; err == nil && len(chapters) > 0 {
			currentIndex := -1
			for i, item := range chapters {
				if item.ID == chapter.ID {
					currentIndex = i
					break
				}
			}
			if currentIndex >= 0 {
				previousStart := currentIndex - 3
				if previousStart < 0 {
					previousStart = 0
				}
				previousChapterLines := []string{}
				for i := previousStart; i < currentIndex; i++ {
					previousChapterLines = append(previousChapterLines, formatChapterContextLine(chapters[i], false))
				}
				appendContextSection("【前序章节大纲｜只用于剧情承接，不主动写进当前画面】", previousChapterLines)

				appendContextSection("【当前章节大纲｜当前生成内容的剧情核心】", []string{
					formatChapterContextLine(chapters[currentIndex], true),
				})

				nextEnd := currentIndex + 3
				if nextEnd >= len(chapters) {
					nextEnd = len(chapters) - 1
				}
				nextChapterLines := []string{}
				for i := currentIndex + 1; i <= nextEnd; i++ {
					nextChapterLines = append(nextChapterLines, formatChapterContextLine(chapters[i], false))
				}
				appendContextSection("【后续章节大纲｜只用于理解剧情方向】", nextChapterLines)
			}
		}
	}

	var scenes []models.Scene
	if err := db.Where("chapter_id = ?", scene.ChapterID).Order("`index` asc, id asc").Find(&scenes).Error; err == nil && len(scenes) > 0 {
		currentIndex := -1
		for i, item := range scenes {
			if item.ID == scene.ID {
				currentIndex = i
				break
			}
		}
		if currentIndex >= 0 {
			previousStart := currentIndex - 3
			if previousStart < 0 {
				previousStart = 0
			}
			previousSceneLines := []string{}
			for i := previousStart; i < currentIndex; i++ {
				previousSceneLines = append(previousSceneLines, formatSceneContextLine(scenes[i], false))
			}
			appendContextSection("【前序场景上下文｜只用于承接，不要主动生成为当前画面】", previousSceneLines)

			appendContextSection("【当前场景上下文｜默认生成范围核心】", []string{
				formatSceneContextLine(scenes[currentIndex], true),
			})

			nextEnd := currentIndex + 4
			if nextEnd >= len(scenes) {
				nextEnd = len(scenes) - 1
			}
			nextSceneLines := []string{}
			for i := currentIndex + 1; i <= nextEnd; i++ {
				nextSceneLines = append(nextSceneLines, formatSceneContextLine(scenes[i], false))
			}
			appendContextSection("【后续1-4个场景上下文｜用户可能合并为长镜头/连续分镜时才纳入生成】", nextSceneLines)
		}
	}

	if len(lines) == 0 {
		return "无"
	}
	return strings.Join(lines, "\n\n")
}

func formatOptimizeReferenceAssets(label string, assets []resolvedAnimationReferenceAsset) string {
	if len(assets) == 0 {
		return label + "：无"
	}
	lines := make([]string, 0, len(assets))
	for i, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" {
			name = fmt.Sprintf("%s %d", label, i+1)
		}
		source := strings.TrimSpace(asset.Source)
		if source == "" {
			source = name
		}
		lines = append(lines, fmt.Sprintf("%s %d：%s（source: %s）", label, i+1, name, source))
	}
	return strings.Join(lines, "\n")
}

func buildAnimationPromptOptimizeUserPrompt(
	scene models.Scene,
	original string,
	narrativeContext string,
	imageAssets []resolvedAnimationReferenceAsset,
	audioAssets []resolvedAnimationReferenceAsset,
	videoAssets []resolvedAnimationReferenceAsset,
	wanVideoModel bool,
) string {
	currentScene := formatSceneContextLine(scene, true)
	instruction := "请根据系统提示中已经列明的优秀样例可迁移规则，优化下面的动画制作提示词。必须严格保留并遵循原始提示词里的所有参考素材引用；若已随请求提供图片，请把图片按顺序视为“图片 1、图片 2...”的视觉上下文。"
	contextRule := "【上下文取舍提醒】\n前序场景只用于承接，不要主动写成当前生成画面；当前场景是默认生成核心；后续1-4个场景只有在原始提示词明显描述连续镜头、多个分镜或长镜头时，才纳入优化后的镜头内容。"
	if wanVideoModel {
		instruction = "请按系统提示中万相 3.0 官方提示词指南的整理规则，把下面的动画制作提示词梳理成【核心任务】【情节概要】【音频风格】【运镜与核心约束】【负面提示词】五段结构化请求。必须严格保留并遵循原始提示词里的所有参考素材引用；若已随请求提供图片，请把图片按顺序视为“图片 1、图片 2...”的视觉上下文，只从中确认可直接观察到的特征；音频与视频素材按原始提示词与下方列表的职责如实整理，不假称已看过或听过。"
		contextRule = "【上下文取舍提醒】\n剧情上下文只用于理解原始提示词的指代与承接关系，不要把原始提示词没写的上下文内容补进请求正文；当前场景是默认整理核心；后续1-4个场景只有在原始提示词明显描述连续镜头、多个分镜或长镜头时，才随情节节点纳入整理。"
	}
	return instruction + "\n\n" +
		"【当前场景摘要】\n" + currentScene + "\n\n" +
		"【剧情上下文】\n" + narrativeContext + "\n\n" +
		contextRule + "\n\n" +
		"【已上传/已引用参考素材】\n以下列表是当前动画片段挂载的可用参考素材；只有原始提示词明确引用、或用户用“图片1/音频1/视频1”等顺序指代、或镜头生成确实需要的素材才写入整理结果，不要为了用完素材而强行加入无关角色/场景。\n" +
		formatOptimizeReferenceAssets("图片", imageAssets) + "\n" +
		formatOptimizeReferenceAssets("音频", audioAssets) + "\n" +
		formatOptimizeReferenceAssets("视频", videoAssets) + "\n\n" +
		"【原始提示词】\n" + strings.TrimSpace(original)
}

// normalizeOptimizedPrompt 清理 LLM 返回的优化/整理结果：去掉代码块围栏、“优化后提示词”类标题前缀与说明性结尾，只留请求正文
func normalizeOptimizedPrompt(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}

	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 1 {
			lines = lines[1:]
		}
		if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			lines = lines[:len(lines)-1]
		}
		text = strings.TrimSpace(strings.Join(lines, "\n"))
	}

	markers := []string{"## ✅ 优化后提示词", "## 优化后提示词", "优化后提示词：", "优化后提示词:"}
	for _, marker := range markers {
		if idx := strings.Index(text, marker); idx >= 0 {
			text = strings.TrimSpace(text[idx+len(marker):])
			break
		}
	}

	cutMarkers := []string{"\n---", "\n## ", "\n### ", "\n修改说明", "\n💡", "\n进阶建议"}
	for _, marker := range cutMarkers {
		if idx := strings.Index(text, marker); idx >= 0 {
			text = strings.TrimSpace(text[:idx])
		}
	}

	text = strings.Trim(text, "` \n\t")
	text = strings.TrimPrefix(text, "提示词：")
	text = strings.TrimPrefix(text, "提示词:")
	return strings.TrimSpace(text)
}

func buildAnimationPromptDraftSystemPrompt() string {
	return `你是视频分镜提示词专家。本次任务的输入是：同一章节内一个或多个连续分镜在剧本创作模块中的文字信息（每个分镜各有画面描述、台词/旁白、镜头运镜、转场剪辑手法，可能附带剧情上下文），以及按顺序编号的分镜参考图（参考图会随请求以"图片1、图片2..."的顺序提供，并标注所属分镜；可能是线稿构图参考，也可能是成熟参考图）。你的任务是：严格遵循下方的核心工作逻辑、输出格式与全部约束，把本次给定的全部连续分镜合并为一条可直接用于单次视频生成（单条视频约可承载 30 秒）的提示词草稿：整条提示词构成一段剧情连贯、镜头自然衔接的完整视频方案，每个分镜至少对应一个"镜头"，全部镜头按分镜先后顺序连续编号；本次只给定一个分镜时，即为该分镜单独生成。

注意：下方约束中的所有示例（东方玄幻、古风武侠、人物姓名、具体场景等）仅用于示范格式、结构、写法密度与细节颗粒度，不代表固定题材。实际输出的题材、世界观、场景与人物必须贴合本次提供的剧本文字信息、参考图与剧情上下文；若上下文不足以判断题材，按剧情文字自然推断，不要生硬照搬示例中的世界观、人物或场景名。

执行补充：若本次未提供参考图，则以剧情文字中的运镜、构图与画面描述为构图依据；若参考图不是线稿，同样只吸收其机位、构图、人物位置与场景布局信息，其余规则不变。

### 核心工作逻辑
输入=**文字剧情脚本 + 线稿构图参考**
工作顺序：先读懂线稿确定机位、画面构图、人物站位、前后景层次、肢体框架、视线方向；再结合你提供的剧情文案填充人物神态、动作发力细节、光影特效、台词；最后按照你固定的成品格式输出高级感动画分镜关键词。
>重要约束：成品文字**不能出现“线稿、草图、红线、轮廓稿”字样**，只吸收线稿的构图布局，直接转化为成熟画面描述，不把草稿痕迹带入关键词。

### 多分镜合并逻辑（本次给定多个分镜时生效）
1. **分镜与镜头的对应**：每个分镜至少输出一个镜头；分镜自带多张参考图时，在该分镜内部按参考图顺序拆分多个镜头；所有镜头全片连续编号（镜头1、镜头2、……），顺序与分镜先后顺序严格一致，禁止调换或跨越分镜顺序。
2. **分镜间转场内化**：相邻分镜标注的“转场剪辑手法”不再是两段视频之间的剪辑手法，而是转化为镜头之间的画面衔接方式（如“快速无缝衔接上一幕”），在单条视频内自然过渡，保证时间、空间、人物状态、光线氛围前后连续。
3. **全量覆盖**：必须完整覆盖本次给定每一个分镜的剧情要点与台词，禁止遗漏、跳过或擅自合并省略任何分镜；同时禁止把给定分镜范围之外的场景、剧情生成为画面。
4. **时长与节奏**：整条视频按分镜数量与剧情密度合理分配节奏，信息密度高、打斗或情绪爆发强的分镜可以给更多镜头与细节，过渡性分镜适当精简，但每个分镜的时长份额要与其剧情重要性匹配，不要头重脚轻。

### 人物判定（决定草稿最后的【出场人物】与【说话人物】两行名单）
1. **出场标准**：人物真实出现在本次给定分镜的画面中（正脸、侧影、背影、局部身影均算出场）。
2. **提及不算出场**：仅在画面描述、台词、旁白、对白或内心活动中被提及、但画面中未出现其身影的人物，不算出场。
3. **说话标准**：人物在本次给定分镜中实际开口（台词、对白、旁白、画外音、内心独白均算说话），无论是否出现在画面中；出场与说话相互独立：出场但无对白的人物不算说话，画外音只闻其声不出场也算说话。
4. **只从人设名单挑选**：出场人物与说话人物都只能从【人物人设名单】中挑选；名单之外的群演、路人、模糊人群不要列入。
5. **音频轨道信号**：若提供了音频轨道角色，说明该角色在本次分镜配置了台词音频，必须列入【说话人物】名单（系统据此自动附加其音色参考）；但其是否列入【出场人物】仍以画面身影为准。
6. **数量上限**：出场人物超过 4 位时，只按画面中的重要程度列出前 4 位；说话人物超过 4 位时，只按台词重要程度列出前 4 位。

### 完整输出格式
镜头 X：【机位+构图（取自线稿）+场景环境+人物站位（严格匹配线稿布局）+肢体姿态（遵循线稿人体框架，增加发力/微表情细节）+道具、灵气光影特效+台词/内心OS】，关键词：【高密度镜头、人物姿态、特效、氛围类名词短语，顿号隔开】，音效：【精准匹配画面节奏的音效，台词放置在此处】。
镜头 X：……（多分镜合并时，每个分镜至少一个镜头，全部镜头按分镜顺序连续编号）
【出场人物】人物A、人物B（草稿最后固定输出出场人物名单：从【人物人设名单】中挑选真实出现在本次分镜画面中的人物，顿号分隔，最多 4 位；人设名单中无人出场或未提供人设名单时输出【出场人物】无）
【说话人物】人物A、人物C（草稿最后固定输出说话人物名单：从【人物人设名单】中挑选在本次分镜中有台词、对白、旁白、画外音或内心独白的人物，顿号分隔，最多 4 位；无人说话或未提供人设名单时输出【说话人物】无）

### 📌线稿吸收细则
1. **机位构图优先取自线稿**
线稿决定镜头类型（俯视/仰视/越肩/特写/跟拍）、画面取景范围、人物在画面左右/上下位置、前景与远景物体分布，不擅自改动原始布局。
2. **人物框架以线稿为基准，做升级美化**
保留线稿给出的身体朝向、手部位置、头脸角度，在此基础上追加肌肉发力感、面部微表情、眼神变化、肢体动态残影、灵气粒子等爆点细节；**不彻底推翻线稿动作重画一套全新姿势**。
3. **多线稿连续镜头处理**
多张线稿依次提供，每张线稿严格绑定其所属分镜与镜头（如分镜 1 的线稿对应镜头1、镜头2，分镜 2 的线稿从镜头3接着连续编号），每个镜头严格绑定对应一张线稿的空间布局，保证镜头切换逻辑和分镜、线稿顺序统一；若你要求追加额外镜头，新增镜头风格、场景、人物和前后线稿画面保持连贯。
4. **剧情与线稿融合原则**
线稿是画面骨架，你的文字剧情是情绪与事件内核。骨架不动，在骨架之上增加炫酷特效、光影层次、动态模糊、灵气光纹、气流冲击波、微表情反差等高级视觉爆点；不擅自增减主线剧情。

### ❌禁止项汇总
1. 禁止在镜头文案里提及线稿、草稿、手绘线条、标注；
2. 禁止脱离线稿构图，随意更改人物站位、镜头角度；
3. 禁止短句碎片化罗列关键词，必须沿用你给的长段镜头描述+独立关键词区块的结构；
4. 禁止空洞笼统描述（“人物很紧张，特效很好看”），所有氛围落实到具体光影、粒子、肢体细节；
5. 禁止额外增加分析、解释、修改说明，只输出最终成品分镜与最后的【出场人物】【说话人物】两行名单；
6. 禁止遗漏、跳过或调换本次给定的任何分镜（多分镜合并时必须逐个覆盖、保持先后顺序）。
7. 禁止在草稿中输出任何整体画风、风格模板类总结文案（如以“整体…”开头的画风总结段）；画风提示词由系统在生成视频时自动追加到提示词末尾。

### 📝工作执行步骤（收到你的需求后执行）
1. 按顺序通读本次给定的全部分镜文字信息，串联完整剧情走向与情绪曲线
2. 解析每一张线稿：锁定机位、构图、人物位置、肢体轮廓、前后景物体，并对应到所属分镜
3. 通读你的文字剧情，确定人物情绪、台词、法术特效、事件走向
4. 将剧情元素嵌入线稿固定画面布局中，丰富动态、光影、灵气、微表情细节，强化高级感与视觉爆点
5. 严格套用你的标准模板分段输出镜头，镜头全片连续编号，分镜之间以内化的衔接方式自然过渡
6. 判断本次分镜的真实出场人物（画面中出现身影才算出场，仅被台词/旁白提及不算）与说话人物（有台词、对白、旁白、画外音或内心独白），在草稿最后分别以【出场人物】…与【说话人物】…格式输出两行名单
7. 检查草稿中没有附带任何整体画风、风格模板类总结文案；画风提示词由系统在生成视频时自动追加到提示词末尾

### 🧪实操示例
>线稿信息：镜头1近景侧斜，女主啃面包，看守弟子手扶肚子；镜头2高空俯视，女主位于人群中心；镜头3面部大特写，女主会心浅笑
>剧情：女主惬意啃法棍，看守弟子肚子鸣叫，杂役动作迟缓，众人馋到吞咽口水，女主会心一笑
镜头 1：侧斜角度拍摄，中近景构图，青石铺就的归一宗广场上，女主连瑟居于画面右侧，双手捧着法棍面包惬意啃食，腮帮子微微鼓起，神态松弛悠然，画面前景左侧是值守的归一宗弟子，他一手不自觉按在腹部，肩膀僵硬紧绷，目光不受控制瞟向面包，耳尖泛起淡淡的红晕，满脸掩饰不住的尴尬，关键词：侧斜机位、中近景构图、古风宗门青石广场、女主连瑟、手持法棍面包、惬意咀嚼、腮部鼓起、归一宗看守弟子、手抚腹部、僵硬拘谨姿态、耳尖泛红、偷偷侧目、克制的馋意、柔和日间自然光，音效：清晰咀嚼声，弟子肚子低沉咕咕叫声。
镜头 2：高空俯视拍摄，广阔的宗门广场，女主连瑟处在画面视觉中心，周围环绕多名杂役弟子，一名杂役拖拽扫帚，扫地动作拖沓迟缓，扫帚在地面停滞滑行，另一名弟子双手扶住木箱，抬箱动作放缓停滞，周围其余弟子纷纷放慢脚步，侧脸悄悄朝向女主方向，所有人喉结不自觉滚动，极力压抑饥饿感，广场散落扫帚、木箱与水桶，秩序微微涣散，关键词：高空俯视机位、广场全景布局、女主位于画面中心、松散环绕的杂役弟子、扫地动作迟缓、扫帚拖曳停滞、搬箱动作停顿、喉结滚动吞咽口水、侧目张望、刻意掩饰馋意、古风宗门殿宇远景、柔和漫射日光、松弛的喜剧反差氛围，音效：拖沓的扫帚摩擦声，此起彼伏连续肚子咕咕叫声，微弱衣物摩擦声。
镜头 3：面部特写拍摄，女主连瑟眉眼舒展，视线缓缓扫过四周一众饥饿窘迫的弟子，嘴角缓缓扬起，露出了然戏谑的会心浅笑，脸颊还残留轻微鼓起，轻声自语调侃众人，画面背景人群被浅景深柔和虚化，关键词：面部特写机位、女主连瑟、了然戏谑浅笑、眼神从容扫视、脸颊轻微鼓起、背景人群虚化、浅景深效果、柔和氛围感光影、轻喜剧情绪落点，音效：细碎咀嚼声，一声轻柔的轻笑，连瑟轻声自语：“看来大家都饿了”。
【出场人物】连瑟、归一宗弟子
【说话人物】连瑟

## 镜头描述标准

### 1. 机位与构图
必须明确写出镜头语言，例如：

- 侧斜角度拍摄
- 中近景构图
- 正面拍摄
- 镜头拉远
- 快速无缝衔接上一幕
- 女主侧后方仰视拍摄
- 高速跟拍剑气运动镜头
- 高角度俯视拍摄
- 斜向俯冲坠落
- 面部特写
- 低角度仰视
- 越肩视角
- 弹道跟随动态镜头

禁止只写“镜头切到”“画面给到”这种空泛表达。

---

### 2. 人物动作
动作必须写清楚发力方式、身体姿态、节奏变化。

例如：

- 身体微微前压
- 左手迅猛向前探出
- 五指骤然发力
- 躯体顺势旋转变向
- 右手紧握长剑迅速蓄能
- 手腕急速连续翻转摆动
- 腰腹积蓄力量猛然迸发
- 铁扇横向全力横扫而出
- 眼神高度专注紧绷
- 眉头紧皱
- 嘴角上扬
- 喉结滚动
- 耳尖泛红

动作不能只写“他攻击了”“她格挡了”“弟子很饿”。

---

### 3. 特效描述
特效必须有形态、颜色、质感、运动轨迹、冲击结果。

例如：

- 三枚白色法术飞弹
- 飞弹表面流转冷白光纹
- 边缘裹挟细碎闪烁灵气颗粒
- 巨型淡蓝色剑气
- 连绵蓝色刃浪
- 宽阔绵长蓝色发光光轨
- 剑气刃状边缘灵光翻涌
- 淡红色结界壁迅速蔓延蛛网般密集裂痕
- 铁扇表层红色灵光骤然暴涨铺展
- 金色火花、白色碎光、灵气爆散
- 环形冲击波向外扩散
- 尘土漫天卷起

特效不能只写“发出光效”“出现剑气”“有灵气”。

---

### 4. 关键词标准
关键词必须是**高密度名词化、镜头提示词风格**，不要长句子。

例如：

- 侧斜机位
- 中近景构图
- 沈竹心愠怒紧绷神态
- 左臂迅猛前伸
- 掌心灵光凝聚
- 三枚白色法术飞弹
- 流动冷白光纹
- 漂浮细碎灵气颗粒
- 短促灵气爆发
- 直线高速激射
- 巨型淡蓝色剑气
- 蓝色刃浪
- 剑气直冲镜头
- 铁扇萦绕淡淡红色灵光
- 连续快速弹击
- 金色火花
- 白色碎光
- 灵气猛烈爆散
- 淡红色结界
- 环形扩散结界波纹
- 弹道跟随动态镜头
- 高速冲击突进
- 无慢动作处理
- 强视觉爆点

关键词之间用顿号分隔。

---

### 5. 音效标准
音效要服务节奏，不能太笼统。

例如：

- 法术凝聚低沉嗡鸣
- 飞弹破空尖锐呼啸
- 掌心劲气爆裂声
- 长剑震颤低沉轰鸣
- 剑气爆发隆隆巨响
- 狂风撕裂呼啸声
- 铁扇连续撞击脆响
- 法术飞弹爆裂声
- 火花噼啪炸裂声
- 强劲冲击波轰鸣巨响
- 剑气破空呼啸
- 能量猛烈爆裂声
- 碎石撞击地面脆响
- 人群慌乱奔跑嘈杂声

如果有台词，台词放在音效前。

---

## 文风标准

### 禁止口语化
不要出现：

- “然后”
- “接下来”
- “准备”
- “好像”
- “看起来”
- “有点”
- “非常”
- “很”
- “大概”
- “那种感觉”
- “水词解释”

---

### 禁止逻辑松散
不要写：

- “女主吃面包，然后弟子饿了”
- “赵长春很生气”
- “大家都馋了”
- “场面很搞笑”

要写成：

- 女主连瑟站在归一宗大广场中央，双手持法棍面包惬意啃食，腮帮子轻微鼓起，神情放松又得意。
- 一名归一宗弟子腹部微微收紧，一只手不自觉摸向肚子，耳尖泛红，目光僵硬地飘向女主手中的面包。
- 赵长春原本威严镇定的面部彻底破功，眉头高高皱起，眼睛瞪大，嘴角抽搐，形成极度惊讶与暴怒交织的颜艺表情。

---

## 打斗镜头重点

打斗镜头必须突出：

- 招式衔接
- 速度感
- 力量感
- 轨迹
- 冲击点
- 特效形态
- 镜头压迫
- 人物表情
- 节奏变化

例如：

- 连招无缝接续
- 动作连贯无停滞
- 高速挥剑
- 剑气直冲镜头
- 连续快速弹击
- 手腕急速回旋
- 横向全力扫扇
- 巨型剑气一并偏转
- 剑气沿切线偏移轨迹
- 冲击波向外扩散
- 剑气冲天急速攀升
- 结界壁蛛网裂纹蔓延
- 被剑气轰然冲破

---

## 喜剧镜头重点

喜剧镜头不能只写“搞笑”，要写反差。

例如：

- 女主惬意啃面包
- 看守弟子表面严肃，实则肚子叫
- 杂役弟子扫地变慢
- 搬箱弟子动作迟缓
- 众弟子偷偷咽口水
- 赵长春从威严到陶醉
- 长老闻到食物气味后脸红
- 原本暴怒的表情被香气软化
- 弟子排队但地面还扔着扫帚木箱

重点是：**严肃秩序被一件小事击穿。**

---

## 线稿使用标准

线稿只吸收以下信息：

- 机位
- 构图
- 人物位置
- 人物数量
- 肢体动作
- 视线方向
- 前后景关系
- 大致场景布局

不要在线稿描述里出现：

- 红线
- 黑线
- 草图
- 线稿
- 草稿
- 标注
- 手绘痕迹
- 未完成感

线稿只是构图依据，输出画面必须是完整成熟的东方玄幻动画分镜。

---

## 台词格式

台词放在动作描述之后，例如：

女主连瑟轻笑喃喃道：“哦～我就知道嘛——”

沈竹心狠厉喝道：“给我乖乖躺下！”

赵长春愤怒大喊：“这是在干什么！”

---

## 画风约束

画风提示词由系统在生成视频时自动追加到提示词末尾，草稿不要输出任何整体画风、风格模板类总结文案：不要以“整体…”开头附加画风总结段，不要罗列质感、色调、画风统一描述，也不要在镜头描述里堆砌画风套话；草稿只聚焦镜头内容本身的机位、构图、动作、特效、氛围与音效。`
}

func (h *AnimationHandler) resolveSceneReferenceImageURL(db *gorm.DB, raw string) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return ""
	}
	if strings.HasPrefix(normalized, "http://") || strings.HasPrefix(normalized, "https://") {
		return normalized
	}
	key := normalizeReferenceFileKey(normalized)
	if key == "" {
		return ""
	}
	file, err := h.findFileByKey(db, key)
	if err != nil {
		return ""
	}
	signedURL, err := h.buildSignedFileURL(file)
	if err != nil {
		return ""
	}
	return signedURL
}

func buildAnimationPromptDraftNarrativeContext(db *gorm.DB, scene models.Scene, chapterScenes []models.Scene, currentIndex int) string {
	var lines []string

	var chapter models.Chapter
	if err := db.First(&chapter, scene.ChapterID).Error; err == nil {
		if synopsis := truncatePromptText(chapter.Synopsis, 1200); synopsis != "" {
			header := "【当前章节大纲｜本次生成的剧情核心】"
			if title := strings.TrimSpace(chapter.Title); title != "" {
				header = "【当前章节大纲｜" + title + "】"
			}
			lines = append(lines, header+"\n"+synopsis)
		}
	}

	if currentIndex > 0 {
		previousStart := currentIndex - 3
		if previousStart < 0 {
			previousStart = 0
		}
		previousLines := []string{}
		for i := previousStart; i < currentIndex; i++ {
			description := truncatePromptText(chapterScenes[i].Description, 500)
			if description == "" {
				description = "（无画面描述）"
			}
			previousLines = append(previousLines, fmt.Sprintf("场景 %.2f：%s", chapterScenes[i].Index, description))
		}
		lines = append(lines, "【前序场景画面描述｜只用于剧情承接与画面连贯，不要生成为当前画面】\n"+strings.Join(previousLines, "\n"))
	}

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n\n")
}

// animationCharacterImageSlot 人物参考图槽位：自动附加时按顺序取第一张已配置的图
type animationCharacterImageSlot struct {
	Field string // 与前端 Character 图像字段名一致（JSON 字段）
	Label string // 槽位中文名
	Value func(character models.Character) string
}

// animationCharacterImageSlots 人物参考图槽位优先级：三视图 → 半身正面 → 全身正面 → 全身侧视图 → 全身后视图
var animationCharacterImageSlots = []animationCharacterImageSlot{
	{Field: "referenceImageUrl", Label: "三视图", Value: func(character models.Character) string { return strings.TrimSpace(character.ReferenceImageUrl) }},
	{Field: "halfBodyFrontImageUrl", Label: "半身正面", Value: func(character models.Character) string { return strings.TrimSpace(character.HalfBodyFrontImageUrl) }},
	{Field: "fullBodyFrontImageUrl", Label: "全身正面", Value: func(character models.Character) string { return strings.TrimSpace(character.FullBodyFrontImageUrl) }},
	{Field: "fullBodySideImageUrl", Label: "全身侧视图", Value: func(character models.Character) string { return strings.TrimSpace(character.FullBodySideImageUrl) }},
	{Field: "fullBodyBackImageUrl", Label: "全身后视图", Value: func(character models.Character) string { return strings.TrimSpace(character.FullBodyBackImageUrl) }},
}

// maxAnimationDraftCharacterReferences 单次草稿自动附加的人物参考上限，避免出场人物过多时挤占视频生成参考位；
// 系统提示中"出场人物名单最多 4 位"的约束需与此保持一致
const maxAnimationDraftCharacterReferences = 4

// maxAnimationDraftCharacterVoiceReferences 单次草稿自动附加音色音频的人物上限（出场且说话、仅说话的人物共用），
// 系统提示中"说话人物名单最多 4 位"的约束需与此保持一致
const maxAnimationDraftCharacterVoiceReferences = 4

// loadAnimationPromptDraftRosterCharacters 加载作品大纲人设的完整人物名单，作为 LLM 判定本场出场/说话人物的候选
func loadAnimationPromptDraftRosterCharacters(db *gorm.DB, scene models.Scene) []models.Character {
	var characters []models.Character
	if err := db.Where("book_id = ?", scene.BookID).Order("`index` asc, id asc").Find(&characters).Error; err != nil {
		return nil
	}
	return characters
}

// listAnimationPromptDraftAudioRoles 汇总本次合并分镜音频轨道的角色名（去重、按轨道顺序），
// 提供给 LLM 标注说话人物，并在后端兜底并入说话名单（已配置台词音频的角色必然有对白）
func listAnimationPromptDraftAudioRoles(db *gorm.DB, mergedSceneIDs []uint) []string {
	if len(mergedSceneIDs) == 0 {
		return nil
	}
	var audioTracks []models.SceneAudio
	if err := db.Where("scene_id IN ?", mergedSceneIDs).Order("scene_id asc, `index` asc").Find(&audioTracks).Error; err != nil {
		return nil
	}
	roles := make([]string, 0, len(audioTracks))
	seen := map[string]bool{}
	for _, track := range audioTracks {
		role := strings.TrimSpace(track.Role)
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		roles = append(roles, role)
	}
	return roles
}

// animationPromptDraftCharacterLineMarkers LLM 按系统提示在草稿末尾标注出场人物名单的行前缀（兼容常见变体）
var animationPromptDraftCharacterLineMarkers = []string{"【出场人物】", "出场人物：", "出场人物:"}

// animationPromptDraftSpeakingLineMarkers LLM 按系统提示在草稿末尾标注说话人物名单的行前缀（兼容常见变体）
var animationPromptDraftSpeakingLineMarkers = []string{"【说话人物】", "说话人物：", "说话人物:"}

// animationPromptDraftCharacterLineContent 判断一行是否为 markers 对应的人物名单行，是则返回标记后的名单内容
func animationPromptDraftCharacterLineContent(line string, markers []string) (string, bool) {
	trimmed := strings.Trim(strings.TrimSpace(line), "*`- ")
	for _, marker := range markers {
		if !strings.HasPrefix(trimmed, marker) {
			continue
		}
		content := strings.TrimSpace(trimmed[len(marker):])
		content = strings.TrimPrefix(content, "：")
		content = strings.TrimPrefix(content, ":")
		return strings.TrimSpace(content), true
	}
	return "", false
}

// animationPromptDraftRosterLine 判断一行是否为出场/说话人物名单行
func animationPromptDraftRosterLine(line string) bool {
	if _, ok := animationPromptDraftCharacterLineContent(line, animationPromptDraftCharacterLineMarkers); ok {
		return true
	}
	_, ok := animationPromptDraftCharacterLineContent(line, animationPromptDraftSpeakingLineMarkers)
	return ok
}

// animationPromptDraftPlaceholderNone 名单行占位内容（表示无人）
func animationPromptDraftPlaceholderNone(content string) bool {
	return content == "无" || content == "（无）" || content == "(无)" || strings.EqualFold(content, "none")
}

// animationPromptDraftNormalizeName 将人物名中的全角括号归一为半角括号（LLM 常混用两种括号），
// 并去除首尾空白
func animationPromptDraftNormalizeName(name string) string {
	name = strings.ReplaceAll(name, "（", "(")
	name = strings.ReplaceAll(name, "）", ")")
	return strings.TrimSpace(name)
}

// animationPromptDraftRootName 取人物名的根名：第一个 "(" 之前的部分（无括号时为整个名字）
func animationPromptDraftRootName(name string) string {
	if idx := strings.Index(name, "("); idx >= 0 {
		return strings.TrimSpace(name[:idx])
	}
	return name
}

// animationPromptDraftSplitRosterNames 按顿号/逗号/分号等分隔名单内容，
// 去掉 LLM 可能附带的引号与首尾空白，并把全角括号归一为半角括号；
// 括号内容可能是名字的一部分（如 "林凡(成年)"），也可能只是模型附带的说明（如 "阿羽(旁白)"），
// 无法在此区分，因此保留完整 token 交给匹配层解析
func animationPromptDraftSplitRosterNames(content string) []string {
	names := make([]string, 0, 4)
	for _, name := range strings.FieldsFunc(content, func(r rune) bool {
		return strings.ContainsRune("、，,；;/ \t", r)
	}) {
		name = strings.Trim(name, "\"'“”‘’ ")
		if name = animationPromptDraftNormalizeName(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// extractAnimationPromptDraftCharacterNames 从草稿文本中解析 LLM 按系统提示标注的出场/说话人物名单行：
// 返回剔除名单行后的草稿文本、出场人物名列表与说话人物名列表（各自保持 LLM 给出的顺序并去重）；
// 未找到名单行或标注为"无"时对应列表为空
func extractAnimationPromptDraftCharacterNames(draft string) (string, []string, []string) {
	lines := strings.Split(draft, "\n")
	appearingNames := []string{}
	speakingNames := []string{}
	appearingSeen := map[string]bool{}
	speakingSeen := map[string]bool{}
	appendNames := func(names *[]string, seen map[string]bool, content string) {
		for _, name := range animationPromptDraftSplitRosterNames(content) {
			if seen[name] {
				continue
			}
			seen[name] = true
			*names = append(*names, name)
		}
	}
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		content, ok := animationPromptDraftCharacterLineContent(line, animationPromptDraftCharacterLineMarkers)
		speakingLine := false
		if !ok {
			content, ok = animationPromptDraftCharacterLineContent(line, animationPromptDraftSpeakingLineMarkers)
			speakingLine = true
		}
		if !ok {
			kept = append(kept, line)
			continue
		}
		// 标记后内容为空时，LLM 可能把名单写在了下一行（下一行本身也是名单行时不消费）
		if content == "" && i+1 < len(lines) {
			if !animationPromptDraftRosterLine(lines[i+1]) {
				content = strings.TrimSpace(lines[i+1])
				i++
			}
		}
		content = strings.Trim(content, "。.;；")
		if content == "" || animationPromptDraftPlaceholderNone(content) {
			continue
		}
		if speakingLine {
			appendNames(&speakingNames, speakingSeen, content)
		} else {
			appendNames(&appearingNames, appearingSeen, content)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), appearingNames, speakingNames
}

// matchAnimationPromptDraftCharacters 在大纲人设名单中查找 LLM 标注的出场/说话人物，按优先级分层匹配：
//  1. 精确匹配：名字与名单条目完全一致（比对前双方都做全角括号归一 + TrimSpace）
//  2. 去尾部说明后匹配：从最后一个括号段开始逐层剥掉尾部括号（如 "林凡(成年)(男主)" → "林凡(成年)" → "林凡"），
//     每层做精确匹配，命中即用
//  3. 唯一根名兜底：剥到只剩根名（第一个 "(" 之前的部分）仍未命中时，仅当名单中该根名的条目恰好唯一才认；
//     有多个变体（如 林凡(少年)/林凡(成年)/林凡(病房)）时不瞎猜，忽略该 token
//
// 匹配失败的 token 静默忽略（不抛错）；返回保持 LLM 给出的顺序；重名人物取名单中排位靠前的一位；
// 同一角色去重（不同写法解析到同一名单条目时只挂一次）
func matchAnimationPromptDraftCharacters(characters []models.Character, names []string) []models.Character {
	byName := make(map[string]models.Character, len(characters))
	for _, character := range characters {
		name := animationPromptDraftNormalizeName(character.Name)
		if name == "" {
			continue
		}
		if _, exists := byName[name]; exists {
			continue
		}
		byName[name] = character
	}
	matched := make([]models.Character, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		matchedName, ok := animationPromptDraftResolveRosterName(byName, animationPromptDraftNormalizeName(name))
		if !ok || seen[matchedName] {
			continue
		}
		seen[matchedName] = true
		matched = append(matched, byName[matchedName])
	}
	return matched
}

// animationPromptDraftResolveRosterName 将 LLM 给出的一个人物名解析到人设名单条目，返回命中的名单条目名；
// 各层匹配均失败时 ok 为 false，调用方静默忽略该名字
func animationPromptDraftResolveRosterName(byName map[string]models.Character, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	// 1. 精确匹配
	if _, ok := byName[name]; ok {
		return name, true
	}
	root := animationPromptDraftRootName(name)
	// 2. 从最后一个括号段开始逐层剥掉尾部括号（说明），每层尝试精确匹配
	candidate := name
	for strings.HasSuffix(candidate, ")") {
		open := strings.LastIndex(candidate, "(")
		if open < 0 {
			break
		}
		candidate = strings.TrimSpace(candidate[:open])
		if candidate == "" {
			break
		}
		if _, ok := byName[candidate]; ok {
			return candidate, true
		}
	}
	// 3. 唯一根名兜底：名单中恰好只有一个该根名的条目才认，多个变体时不瞎猜
	if root == "" {
		return "", false
	}
	if _, ok := byName[root]; ok {
		return root, true
	}
	unique := ""
	count := 0
	for rosterName := range byName {
		if animationPromptDraftRootName(rosterName) == root {
			count++
			unique = rosterName
		}
	}
	if count == 1 {
		return unique, true
	}
	return "", false
}

// buildAnimationPromptDraftCharacterContext 组装人物人设名单上下文：LLM 依据名单保持出场人物形象一致，
// 并在草稿末尾分别标注本场真实出场/说话的人物（提及不等于出场，画外音只算说话）；音频轨道角色一并提供
func buildAnimationPromptDraftCharacterContext(characters []models.Character, audioRoles []string) string {
	lines := make([]string, 0, len(characters))
	for _, character := range characters {
		name := strings.TrimSpace(character.Name)
		if name == "" {
			continue
		}
		features := strings.TrimSpace(character.CoreFeatures)
		if features == "" {
			features = truncatePromptText(character.Description, 500)
		}
		line := "- " + name
		if features != "" {
			line += "：" + features
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	context := "【人物人设名单｜来自大纲人设模块，为本作品全部人物：草稿中出场人物的外貌、发型、服装等核心特征必须与其设定一致；名单中未在本次分镜画面出现的人物禁止写入草稿；草稿最后的出场/说话人物名单只能从此名单中挑选】\n" + strings.Join(lines, "\n")
	if len(audioRoles) > 0 {
		context += "\n\n【本次合并分镜的音频轨道角色｜这些角色在本次分镜配置了台词音频，必须列入草稿最后的【说话人物】名单，系统据此自动附加音色参考；有台词不等于画面出场，是否列入【出场人物】仍以画面身影为准】\n" + strings.Join(audioRoles, "、")
	}
	return context
}

// mergeAnimationPromptDraftSpeakingNames 将音频轨道角色并入 LLM 标注的说话人物名单（去重、保序）：
// 已配置台词音频的角色必然有对白，作为 LLM 漏标说话人物时的兜底信号
func mergeAnimationPromptDraftSpeakingNames(speakingNames []string, audioRoles []string) []string {
	seen := make(map[string]bool, len(speakingNames)+len(audioRoles))
	merged := make([]string, 0, len(speakingNames)+len(audioRoles))
	for _, list := range [][]string{speakingNames, audioRoles} {
		for _, name := range list {
			if seen[name] {
				continue
			}
			seen[name] = true
			merged = append(merged, name)
		}
	}
	return merged
}

// buildAnimationPromptDraftCharacterReferences 依据 LLM 判定的出场/说话人物分别附加参考：
// 出场人物按槽位优先级附加参考图，说话人物附加音色音频；出场与说话相互独立——
// 出场但无对白的人物只附加参考图，画外音等只说话不出场的人物只附加音色
func buildAnimationPromptDraftCharacterReferences(appearing []models.Character, speaking []models.Character) []models.AnimationPromptDraftCharacterReference {
	speakingByID := make(map[uint]bool, len(speaking))
	for _, character := range speaking {
		speakingByID[character.ID] = true
	}

	references := make([]models.AnimationPromptDraftCharacterReference, 0, len(appearing)+len(speaking))
	appended := make(map[uint]bool, len(appearing)+len(speaking))
	voiceAttached := 0

	// 出场人物：附加参考图；其中同时说话的人物附加音色
	for _, character := range appearing {
		if len(references) >= maxAnimationDraftCharacterReferences {
			break
		}
		reference := models.AnimationPromptDraftCharacterReference{
			CharacterID:  character.ID,
			Name:         strings.TrimSpace(character.Name),
			CoreFeatures: strings.TrimSpace(character.CoreFeatures),
			Appears:      true,
			Speaking:     speakingByID[character.ID],
		}
		for _, slot := range animationCharacterImageSlots {
			if value := slot.Value(character); value != "" {
				reference.ImageKey = value
				reference.ImageSlot = slot.Field
				reference.ImageSlotLabel = slot.Label
				break
			}
		}
		if reference.Speaking {
			reference.VoiceAudioKey = strings.TrimSpace(character.VoiceAudioUrl)
		}
		// 出场但未配置参考图且不说话：无参考可附加，跳过
		if reference.ImageKey == "" && reference.VoiceAudioKey == "" {
			continue
		}
		if reference.VoiceAudioKey != "" {
			voiceAttached++
		}
		appended[character.ID] = true
		references = append(references, reference)
	}

	// 仅说话的人物（画外音/旁白等不出场）：只附加音色，不附加参考图
	for _, character := range speaking {
		if appended[character.ID] {
			continue
		}
		if voiceAttached >= maxAnimationDraftCharacterVoiceReferences {
			break
		}
		voiceKey := strings.TrimSpace(character.VoiceAudioUrl)
		// 未配置音色的画外音人物：无参考可附加，跳过
		if voiceKey == "" {
			continue
		}
		appended[character.ID] = true
		voiceAttached++
		references = append(references, models.AnimationPromptDraftCharacterReference{
			CharacterID:   character.ID,
			Name:          strings.TrimSpace(character.Name),
			CoreFeatures:  strings.TrimSpace(character.CoreFeatures),
			Appears:       false,
			Speaking:      true,
			VoiceAudioKey: voiceKey,
		})
	}
	return references
}

// animationPromptDraftReference 分镜参考图视图（HasImage 表示该条参考图已随请求提供图片）
type animationPromptDraftReference struct {
	Description string
	HasImage    bool
}

// animationPromptDraftScene 参与本次提示词草稿合并的分镜视图
type animationPromptDraftScene struct {
	Position   int // 在本次合并范围内的序号，从 1 开始（1 = 当前分镜）
	Scene      models.Scene
	References []animationPromptDraftReference
}

func buildAnimationPromptDraftUserPrompt(
	draftScenes []animationPromptDraftScene,
	narrativeContext string,
	characterContext string,
) string {
	instruction := "请根据系统提示中的全部约束，结合下方剧本创作信息与参考图，生成本场景的视频提示词草稿。"
	if len(draftScenes) > 1 {
		instruction = fmt.Sprintf(
			"请根据系统提示中的全部约束，结合下方剧本创作信息与参考图，把以下 %d 个连续分镜合并为一条视频提示词草稿：每个分镜至少对应一个镜头，全部镜头按分镜顺序连续编号，禁止遗漏任何分镜。",
			len(draftScenes),
		)
	}
	sections := []string{instruction}

	sceneBlocks := make([]string, 0, len(draftScenes))
	for _, draftScene := range draftScenes {
		scene := draftScene.Scene
		sceneLines := []string{}
		if text := strings.TrimSpace(scene.Description); text != "" {
			sceneLines = append(sceneLines, "画面描述："+text)
		}
		if text := strings.TrimSpace(scene.CameraMovement); text != "" {
			sceneLines = append(sceneLines, "镜头运镜："+text)
		}
		if text := strings.TrimSpace(scene.Dialogue); text != "" {
			sceneLines = append(sceneLines, "台词/旁白："+text)
		}
		if text := strings.TrimSpace(scene.TransitionEffect); text != "" {
			sceneLines = append(sceneLines, "转场剪辑手法："+text)
		}
		if len(sceneLines) == 0 {
			sceneLines = append(sceneLines, "（本分镜暂无画面描述等文字信息，请结合其参考图与前后分镜剧情自然推断）")
		}
		header := fmt.Sprintf("—— 分镜 %d", draftScene.Position)
		if draftScene.Position == 1 {
			header += "（当前分镜，起始）"
		}
		sceneBlocks = append(sceneBlocks, header+" ——\n"+strings.Join(sceneLines, "\n"))
	}
	sections = append(sections, "【分镜文字信息｜剧本创作模块，按剧情先后顺序排列】\n"+strings.Join(sceneBlocks, "\n\n"))

	if strings.TrimSpace(characterContext) != "" {
		sections = append(sections, characterContext)
	}

	referenceLines := []string{}
	imageIndex := 0
	for _, draftScene := range draftScenes {
		for _, reference := range draftScene.References {
			var line string
			if reference.HasImage {
				imageIndex++
				line = fmt.Sprintf("图片 %d（分镜 %d）", imageIndex, draftScene.Position)
				if reference.Description != "" {
					line += "：" + reference.Description
				}
			} else {
				line = fmt.Sprintf("分镜 %d 补充参考（未随请求提供图片）", draftScene.Position)
				if reference.Description != "" {
					line += "：" + reference.Description
				}
			}
			referenceLines = append(referenceLines, line)
		}
	}
	if len(referenceLines) > 0 {
		sections = append(sections, "【分镜参考图｜已按顺序随请求提供图片，图片 N 对应下述第 N 个图片条目，并标注所属分镜】\n"+strings.Join(referenceLines, "\n"))
	}
	if strings.TrimSpace(narrativeContext) != "" {
		sections = append(sections, "【剧情上下文｜只用于剧情承接与情绪理解，不要把合并范围之外的场景主动生成为当前画面】\n"+narrativeContext)
	}
	return strings.Join(sections, "\n\n")
}

// OptimizePrompt 使用 LLM 优化动画提示词，并引入前后章节/场景与参考图片上下文。
// 目标视频模型为万相（wan3.0-*）系列时，按万相 3.0 官方提示词指南整理为五段结构化请求；
// 其余模型沿用电影分镜范式优化。
func (h *AnimationHandler) OptimizePrompt(c *gin.Context) {
	sceneId := c.Param("sceneId")

	if strings.TrimSpace(config.Cfg.ArkAgentPlan.APIKey) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Ark Agent Plan LLM service is not configured"})
		return
	}

	db := database.GetDB()
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
		return
	}

	var req models.OptimizeSceneAnimationPromptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	original := strings.TrimSpace(req.Text)
	if original == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}

	modelID := strings.TrimSpace(req.Model)
	if modelID == "" && len(config.Cfg.ArkAgentPlan.SupportedLLMModels) > 0 {
		modelID = config.Cfg.ArkAgentPlan.SupportedLLMModels[0]
	}
	if modelID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS 未配置"})
		return
	}
	if !arkLLMModelSupported(modelID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前 Ark Agent Plan LLM 模型不在 ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS 中"})
		return
	}

	imageAssets, err := h.resolveReferenceAssets(db, req.ReferenceImageKeys, "图片参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference image not found"})
		return
	}
	audioAssets, err := h.resolveReferenceAssets(db, req.ReferenceAudioKeys, "音频参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference audio not found"})
		return
	}
	videoAssets, err := h.resolveReferenceAssets(db, req.ReferenceVideoKeys, "视频参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference video not found"})
		return
	}

	imageURLs := make([]string, 0, len(imageAssets))
	for _, asset := range imageAssets {
		if signedURL := strings.TrimSpace(asset.SignedURL); signedURL != "" {
			imageURLs = append(imageURLs, signedURL)
		}
		if len(imageURLs) >= 8 {
			break
		}
	}

	// 视频生成模型决定优化方案：万相系列按万相 3.0 官方提示词指南整理为五段结构化请求，其余按电影分镜范式优化
	wanVideoModel := isWanAnimationModel(strings.TrimSpace(req.VideoModel))
	systemPrompt := buildSeedanceAnimationPromptOptimizeSystemPrompt()
	if wanVideoModel {
		systemPrompt = buildWanAnimationPromptOptimizeSystemPrompt()
	}

	client := ai.NewArkClientWithTimeout(config.Cfg.ArkAgentPlan.APIBaseURL, config.Cfg.ArkAgentPlan.APIKey, animationPromptLLMTimeout)
	optimized, err := client.GenerateTextWithImages(
		c.Request.Context(),
		modelID,
		systemPrompt,
		buildAnimationPromptOptimizeUserPrompt(
			scene,
			original,
			buildAnimationPromptOptimizeNarrativeContext(db, scene),
			imageAssets,
			audioAssets,
			videoAssets,
			wanVideoModel,
		),
		imageURLs,
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "提示词优化失败: " + err.Error()})
		return
	}

	optimized = normalizeOptimizedPrompt(optimized)
	if optimized == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "提示词优化结果为空"})
		return
	}

	c.JSON(http.StatusOK, models.OptimizeSceneAnimationPromptResponse{
		Prompt: optimized,
		Model:  modelID,
	})
}

// GeneratePromptDraft 根据剧本创作模块的分镜信息（画面描述/台词/运镜/转场）与场景参考图，用 LLM 生成视频提示词草稿；
// count > 1 时从当前分镜起合并后续连续分镜为一条提示词，用于单次视频生成覆盖多段分镜。
// 出场/说话人物由 LLM 生成草稿时自行判定（画面出现才算出场，实际开口才算说话），在草稿末行分别标注名单，
// 后端解析两份名单并随响应返回人物参考（出场→参考图、说话→音色，二者独立），不再按名字关键词匹配
func (h *AnimationHandler) GeneratePromptDraft(c *gin.Context) {
	sceneId := c.Param("sceneId")

	if strings.TrimSpace(config.Cfg.ArkAgentPlan.APIKey) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Ark Agent Plan LLM service is not configured"})
		return
	}

	db := database.GetDB()
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
		return
	}

	var req models.GenerateSceneAnimationPromptDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	modelID := strings.TrimSpace(req.Model)
	if modelID == "" && len(config.Cfg.ArkAgentPlan.SupportedLLMModels) > 0 {
		modelID = config.Cfg.ArkAgentPlan.SupportedLLMModels[0]
	}
	if modelID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS 未配置"})
		return
	}
	if !arkLLMModelSupported(modelID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前 Ark Agent Plan LLM 模型不在 ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS 中"})
		return
	}

	// 合并分镜数量（含当前分镜），取值 1-10
	mergeCount := req.Count
	if mergeCount < 1 {
		mergeCount = 1
	}
	if mergeCount > 10 {
		mergeCount = 10
	}

	// 加载当前章节全部分镜，取当前分镜起的连续 mergeCount 段（章节剩余不足时按实际数量合并）
	var chapterScenes []models.Scene
	if err := db.Where("chapter_id = ?", scene.ChapterID).Order("`index` asc, id asc").Find(&chapterScenes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch chapter scenes"})
		return
	}
	currentIndex := -1
	for i := range chapterScenes {
		if chapterScenes[i].ID == scene.ID {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		chapterScenes = []models.Scene{scene}
		currentIndex = 0
	}
	mergeEnd := currentIndex + mergeCount
	if mergeEnd > len(chapterScenes) {
		mergeEnd = len(chapterScenes)
	}
	mergedScenes := chapterScenes[currentIndex:mergeEnd]

	// 汇总各分镜参考图，保持分镜内 index 顺序
	mergedSceneIDs := make([]uint, 0, len(mergedScenes))
	for _, item := range mergedScenes {
		mergedSceneIDs = append(mergedSceneIDs, item.ID)
	}
	var allReferences []models.SceneReference
	if err := db.Where("scene_id IN ?", mergedSceneIDs).Order("scene_id asc, `index` asc").Find(&allReferences).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch scene references"})
		return
	}
	referencesByScene := make(map[uint][]models.SceneReference, len(mergedScenes))
	for _, reference := range allReferences {
		referencesByScene[reference.SceneID] = append(referencesByScene[reference.SceneID], reference)
	}

	hasScriptInfo := false
	for _, item := range mergedScenes {
		if strings.TrimSpace(item.Description) != "" ||
			strings.TrimSpace(item.CameraMovement) != "" ||
			strings.TrimSpace(item.Dialogue) != "" ||
			strings.TrimSpace(item.TransitionEffect) != "" {
			hasScriptInfo = true
			break
		}
	}
	hasReferenceInfo := false
	for _, references := range referencesByScene {
		for _, reference := range references {
			if strings.TrimSpace(reference.ImageUrl) != "" || strings.TrimSpace(reference.Description) != "" {
				hasReferenceInfo = true
				break
			}
		}
		if hasReferenceInfo {
			break
		}
	}
	if !hasScriptInfo && !hasReferenceInfo {
		if len(mergedScenes) > 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("当前分镜及其后续共 %d 段分镜均缺少画面描述等剧本创作信息，无法生成提示词草稿", len(mergedScenes))})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "当前场景缺少画面描述等剧本创作信息，无法生成提示词草稿"})
		}
		return
	}

	// 人物人设名单与音频轨道角色：作为 LLM 判定本场出场/说话人物的上下文；
	// 两份名单由 LLM 在草稿末行标注，生成后解析（出场与说话独立：提及不等于出场，画外音只算说话）
	rosterCharacters := loadAnimationPromptDraftRosterCharacters(db, scene)
	audioRoles := listAnimationPromptDraftAudioRoles(db, mergedSceneIDs)
	characterContext := buildAnimationPromptDraftCharacterContext(rosterCharacters, audioRoles)

	// 组装随请求提供的参考图：按分镜顺序、每分镜内参考图顺序，最多 8 张
	imageURLs := make([]string, 0, len(allReferences))
	draftScenes := make([]animationPromptDraftScene, 0, len(mergedScenes))
	for position, item := range mergedScenes {
		draftScene := animationPromptDraftScene{Position: position + 1, Scene: item}
		for _, reference := range referencesByScene[item.ID] {
			hasImage := false
			if strings.TrimSpace(reference.ImageUrl) != "" && len(imageURLs) < 8 {
				if signedURL := h.resolveSceneReferenceImageURL(db, reference.ImageUrl); signedURL != "" {
					imageURLs = append(imageURLs, signedURL)
					hasImage = true
				}
			}
			// 未随请求提供图片的参考：仅有文字描述时仍作为构图上下文提供
			if hasImage || strings.TrimSpace(reference.Description) != "" {
				draftScene.References = append(draftScene.References, animationPromptDraftReference{
					Description: strings.TrimSpace(reference.Description),
					HasImage:    hasImage,
				})
			}
		}
		draftScenes = append(draftScenes, draftScene)
	}

	client := ai.NewArkClientWithTimeout(config.Cfg.ArkAgentPlan.APIBaseURL, config.Cfg.ArkAgentPlan.APIKey, animationPromptLLMTimeout)
	draft, err := client.GenerateTextWithImages(
		c.Request.Context(),
		modelID,
		buildAnimationPromptDraftSystemPrompt(),
		buildAnimationPromptDraftUserPrompt(
			draftScenes,
			buildAnimationPromptDraftNarrativeContext(db, scene, chapterScenes, currentIndex),
			characterContext,
		),
		imageURLs,
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "提示词草稿生成失败: " + err.Error()})
		return
	}

	// 解析 LLM 标注的出场/说话人物名单，并把名单行从草稿中剔除（不进入最终提示词）；
	// 已配置音频轨道的角色必然有台词音频，兜底并入说话名单（LLM 漏标时仍附加音色）
	draft, appearingNames, speakingNames := extractAnimationPromptDraftCharacterNames(draft)
	draft = normalizeOptimizedPrompt(draft)
	if draft == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "提示词草稿生成结果为空"})
		return
	}

	characterReferences := buildAnimationPromptDraftCharacterReferences(
		matchAnimationPromptDraftCharacters(rosterCharacters, appearingNames),
		matchAnimationPromptDraftCharacters(rosterCharacters, mergeAnimationPromptDraftSpeakingNames(speakingNames, audioRoles)),
	)

	c.JSON(http.StatusOK, models.GenerateSceneAnimationPromptDraftResponse{
		Prompt:              draft,
		Model:               modelID,
		SceneCount:          len(mergedScenes),
		CharacterReferences: characterReferences,
	})
}

// List 获取场景的所有动画
// @Summary 获取动画列表
// @Description 获取指定场景的所有动画
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Success 200 {object} models.SceneAnimationListResponse
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations [get]
func (h *AnimationHandler) List(c *gin.Context) {
	sceneId := c.Param("sceneId")

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	var animations []models.SceneAnimation
	if err := db.Where("scene_id = ?", scene.ID).
		Order("`index` ASC").
		Find(&animations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to fetch animations",
		})
		return
	}

	c.JSON(http.StatusOK, models.SceneAnimationListResponse{
		Total: int64(len(animations)),
		Data:  animations,
	})
}

// Create 创建新的动画
// @Summary 创建动画
// @Description 为场景创建新的动画
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animation body models.CreateSceneAnimationRequest true "动画信息"
// @Success 201 {object} models.SceneAnimation
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations [post]
func (h *AnimationHandler) Create(c *gin.Context) {
	sceneId := c.Param("sceneId")
	sceneIdUint, err := strconv.ParseUint(sceneId, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid scene ID",
		})
		return
	}

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	var req models.CreateSceneAnimationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	animation := models.SceneAnimation{
		SceneID: uint(sceneIdUint),
		Name:    req.Name,
		Index:   req.Index,
	}

	if err := db.Create(&animation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create animation",
		})
		return
	}

	c.JSON(http.StatusCreated, animation)
}

// GetByID 获取单个动画详情
// @Summary 获取动画详情
// @Description 获取指定动画的详细信息
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Success 200 {object} models.SceneAnimation
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId} [get]
func (h *AnimationHandler) GetByID(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	c.JSON(http.StatusOK, animation)
}

// Update 更新动画信息（名称、排序等）
// @Summary 更新动画
// @Description 更新动画的基本信息
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Param animation body models.UpdateSceneAnimationRequest true "更新信息"
// @Success 200 {object} models.SceneAnimation
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId} [put]
func (h *AnimationHandler) Update(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	var req models.UpdateSceneAnimationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Index != nil {
		updates["`index`"] = *req.Index
	}

	if len(updates) > 0 {
		if err := db.Model(&animation).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to update animation",
			})
			return
		}
	}

	// 重新加载
	db.First(&animation, animation.ID)

	c.JSON(http.StatusOK, animation)
}

// Delete 删除动画
// @Summary 删除动画
// @Description 删除指定的动画及其版本历史
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId} [delete]
func (h *AnimationHandler) Delete(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	// 开始事务：删除版本历史和动画
	tx := db.Begin()

	// 删除版本历史
	if err := tx.Where("scene_animation_id = ?", animation.ID).Delete(&models.SceneAnimationVersion{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete animation versions",
		})
		return
	}

	if err := tx.Where("scene_animation_id = ?", animation.ID).Delete(&models.SceneAnimationGenerationTask{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete animation generation tasks",
		})
		return
	}

	// 删除动画
	if err := tx.Delete(&animation).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete animation",
		})
		return
	}

	tx.Commit()

	c.JSON(http.StatusOK, gin.H{
		"message": "Animation deleted successfully",
	})
}

// Upload 上传新版本动画
// @Summary 上传动画
// @Description 为动画上传新版本视频
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Param animation body models.UploadAnimationRequest true "动画URL"
// @Success 200 {object} models.SceneAnimationVersion
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId}/upload [put]
func (h *AnimationHandler) Upload(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")
	animationIdUint, err := strconv.ParseUint(animationId, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid animation ID",
		})
		return
	}

	// 获取当前用户ID
	userId, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	// 检查动画是否存在
	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	var req models.UploadAnimationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 获取当前最大版本号（含软删除版本，避免版本号复用）
	var maxVersion int
	db.Unscoped().Model(&models.SceneAnimationVersion{}).
		Where("scene_animation_id = ?", animation.ID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&maxVersion)

	newVersion := maxVersion + 1

	// 开始事务
	tx := db.Begin()

	// 创建新版本记录
	version := models.SceneAnimationVersion{
		SceneAnimationID: uint(animationIdUint),
		VideoUrl:         req.VideoUrl,
		Version:          newVersion,
		CreatedBy:        userId.(uint),
	}

	if err := tx.Create(&version).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create version",
		})
		return
	}

	// 更新动画的当前视频
	updates := map[string]interface{}{
		"animation_url":     req.VideoUrl,
		"animation_version": newVersion,
	}

	if err := tx.Model(&animation).Updates(updates).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update animation",
		})
		return
	}

	tx.Commit()

	c.JSON(http.StatusOK, version)
}

// ListGenerationTasks 获取动画生成任务列表
func (h *AnimationHandler) ListGenerationTasks(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	db := database.GetDB()
	scene, animation, err := h.getSceneAndAnimation(db, sceneId, animationId)
	if err != nil {
		if err.Error() == "scene" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Animation not found"})
		return
	}

	var tasks []models.SceneAnimationGenerationTask
	if err := db.Where("scene_id = ? AND scene_animation_id = ?", scene.ID, animation.ID).
		Order("created_at DESC").
		Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch generation tasks"})
		return
	}

	hydrateAnimationTasks(tasks)
	c.JSON(http.StatusOK, models.SceneAnimationGenerationTaskListResponse{
		Total: int64(len(tasks)),
		Data:  tasks,
	})
}

// GetGenerationTask 获取单个动画生成任务
func (h *AnimationHandler) GetGenerationTask(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")
	taskId := c.Param("taskId")

	db := database.GetDB()
	scene, animation, err := h.getSceneAndAnimation(db, sceneId, animationId)
	if err != nil {
		if err.Error() == "scene" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Animation not found"})
		return
	}

	var task models.SceneAnimationGenerationTask
	if err := db.Where("id = ? AND scene_id = ? AND scene_animation_id = ?", taskId, scene.ID, animation.ID).
		First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Generation task not found"})
		return
	}

	hydrateAnimationTask(&task)
	c.JSON(http.StatusOK, task)
}

// CreateGenerationTask 创建动画生成任务
func (h *AnimationHandler) CreateGenerationTask(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	userIDValue, userIDExists := c.Get("userId")
	if !userIDExists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	userID := userIDValue.(uint)

	if oss.GetClient() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File service is not configured"})
		return
	}

	db := database.GetDB()
	scene, animation, err := h.getSceneAndAnimation(db, sceneId, animationId)
	if err != nil {
		if err.Error() == "scene" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Animation not found"})
		return
	}

	var req models.GenerateSceneAnimationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}
	if _, ok := allowedAnimationRatios[req.Ratio]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ratio must be one of: 16:9, 9:16"})
		return
	}
	// 万相模型 duration 支持传 -1，表示智能时长（由模型根据提示词自动决定输出时长）
	if err := validateAnimationDuration(req.Model, req.Duration); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, ok := allowedAnimationModels[req.Model]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is not supported"})
		return
	}
	if !isAnimationProviderConfigured(req.Model) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": animationProviderName(req.Model) + " video generation service is not configured",
		})
		return
	}

	referenceImageAssets, err := h.resolveReferenceAssets(db, req.ReferenceImageKeys, "图片参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference image not found"})
		return
	}
	referenceAudioAssets, err := h.resolveReferenceAssets(db, req.ReferenceAudioKeys, "音频参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference audio not found"})
		return
	}
	referenceVideoAssets, err := h.resolveReferenceAssets(db, req.ReferenceVideoKeys, "视频参考")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reference video not found"})
		return
	}

	imageTaskAssets := make([]models.AnimationTaskReferenceAsset, 0, len(referenceImageAssets))
	for _, item := range referenceImageAssets {
		imageTaskAssets = append(imageTaskAssets, models.AnimationTaskReferenceAsset{
			Source:   item.Source,
			Name:     item.Name,
			MimeType: item.MimeType,
		})
	}
	audioTaskAssets := make([]models.AnimationTaskReferenceAsset, 0, len(referenceAudioAssets))
	for _, item := range referenceAudioAssets {
		audioTaskAssets = append(audioTaskAssets, models.AnimationTaskReferenceAsset{
			Source:   item.Source,
			Name:     item.Name,
			MimeType: item.MimeType,
		})
	}
	videoTaskAssets := make([]models.AnimationTaskReferenceAsset, 0, len(referenceVideoAssets))
	for _, item := range referenceVideoAssets {
		videoTaskAssets = append(videoTaskAssets, models.AnimationTaskReferenceAsset{
			Source:   item.Source,
			Name:     item.Name,
			MimeType: item.MimeType,
		})
	}

	// “添加到提示词末尾”开关默认打开：请求省略 appendArtStyle 字段时同样按开启处理
	appendArtStyle := resolveAppendArtStyle(req.AppendArtStyle)

	task := models.SceneAnimationGenerationTask{
		SceneID:                  scene.ID,
		SceneAnimationID:         animation.ID,
		Status:                   models.AnimationTaskStatusPending,
		Text:                     text,
		AppendArtStyle:           appendArtStyle,
		Ratio:                    req.Ratio,
		Duration:                 req.Duration,
		Model:                    req.Model,
		ReferenceImageKeysJSON:   encodeStringSlice(req.ReferenceImageKeys),
		ReferenceAudioKeysJSON:   encodeStringSlice(req.ReferenceAudioKeys),
		ReferenceVideoKeysJSON:   encodeStringSlice(req.ReferenceVideoKeys),
		ReferenceImageAssetsJSON: encodeReferenceAssets(imageTaskAssets),
		ReferenceAudioAssetsJSON: encodeReferenceAssets(audioTaskAssets),
		ReferenceVideoAssetsJSON: encodeReferenceAssets(videoTaskAssets),
		CreatedBy:                userID,
	}
	if err := db.Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create generation task"})
		return
	}

	// 开启“添加到提示词末尾”且作品已配置画风提示词时，自动把画风提示词追加到提交给视频模型的提示词末尾
	finalPrompt := buildFinalGenerationPrompt(text, h.resolveBookArtStyle(db, scene), appendArtStyle)

	remoteTaskID, err := h.createRemoteGenerationTask(
		c.Request.Context(),
		&task,
		finalPrompt,
		referenceImageAssets,
		referenceVideoAssets,
		referenceAudioAssets,
	)
	if err != nil {
		message := err.Error()
		now := time.Now()
		db.Model(&task).Updates(map[string]any{
			"status":        models.AnimationTaskStatusFailed,
			"error_message": message,
			"completed_at":  &now,
		})
		task.Status = models.AnimationTaskStatusFailed
		task.ErrorMessage = message
		task.CompletedAt = &now
		hydrateAnimationTask(&task)
		c.JSON(http.StatusCreated, task)
		return
	}

	if err := db.Model(&task).Updates(map[string]any{
		"ark_task_id":   remoteTaskID,
		"status":        models.AnimationTaskStatusProcessing,
		"error_message": "",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update generation task"})
		return
	}

	task.ArkTaskID = remoteTaskID
	task.Status = models.AnimationTaskStatusProcessing
	task.ErrorMessage = ""
	hydrateAnimationTask(&task)
	c.JSON(http.StatusCreated, task)
}

// PollGenerationTask 轮询动画生成任务状态
func (h *AnimationHandler) PollGenerationTask(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")
	taskId := c.Param("taskId")

	if _, userIDExists := c.Get("userId"); !userIDExists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}

	if oss.GetClient() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "File service is not configured"})
		return
	}

	db := database.GetDB()
	scene, animation, err := h.getSceneAndAnimation(db, sceneId, animationId)
	if err != nil {
		if err.Error() == "scene" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Scene not found"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Animation not found"})
		return
	}

	var task models.SceneAnimationGenerationTask
	if err := db.Where("id = ? AND scene_id = ? AND scene_animation_id = ?", taskId, scene.ID, animation.ID).
		First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Generation task not found"})
		return
	}

	if task.Status == models.AnimationTaskStatusSucceeded || task.Status == models.AnimationTaskStatusFailed {
		hydrateAnimationTask(&task)
		c.JSON(http.StatusOK, task)
		return
	}
	if strings.TrimSpace(task.ArkTaskID) == "" {
		hydrateAnimationTask(&task)
		c.JSON(http.StatusOK, task)
		return
	}
	if !isAnimationProviderConfigured(task.Model) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": animationProviderName(task.Model) + " video generation service is not configured",
		})
		return
	}

	httpClient := &http.Client{Timeout: animationTaskPollRequestTimeout}
	remoteStatus, err := h.fetchRemoteTaskStatus(c.Request.Context(), httpClient, &task)
	if err != nil {
		if recordErr := recordArkPollError(db, &task, err); recordErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": recordErr.Error()})
			return
		}
		hydrateAnimationTask(&task)
		c.JSON(http.StatusOK, task)
		return
	}

	if err := h.applyRemoteGenerationTaskStatus(db, &task, remoteStatus); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	hydrateAnimationTask(&task)
	c.JSON(http.StatusOK, task)
}

// ListVersions 获取动画版本历史
// @Summary 获取动画版本历史
// @Description 获取指定动画的所有版本历史
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Success 200 {object} models.SceneAnimationVersionListResponse
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId}/versions [get]
func (h *AnimationHandler) ListVersions(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	// 检查动画是否存在
	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	var versions []models.SceneAnimationVersion
	if err := db.Where("scene_animation_id = ?", animation.ID).
		Order("version DESC").
		Find(&versions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to fetch versions",
		})
		return
	}

	c.JSON(http.StatusOK, models.SceneAnimationVersionListResponse{
		Total: int64(len(versions)),
		Data:  versions,
	})
}

// Revert 回滚动画到指定版本
// @Summary 回滚动画
// @Description 将动画回滚到指定版本
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Param version path int true "版本号"
// @Success 200 {object} models.SceneAnimation
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId}/revert/{version} [put]
func (h *AnimationHandler) Revert(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")
	versionStr := c.Param("version")
	targetVersion, err := strconv.Atoi(versionStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid version number",
		})
		return
	}

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	// 检查动画是否存在
	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	// 获取目标版本
	var version models.SceneAnimationVersion
	if err := db.Where("scene_animation_id = ? AND version = ?",
		animation.ID, targetVersion).First(&version).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Version not found",
		})
		return
	}

	// 更新动画的当前视频
	updates := map[string]interface{}{
		"animation_url":     version.VideoUrl,
		"animation_version": version.Version,
	}

	if err := db.Model(&animation).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to revert animation",
		})
		return
	}

	// 重新加载
	db.First(&animation, animation.ID)

	c.JSON(http.StatusOK, animation)
}

// DeleteVersion 软删除动画历史版本
// @Summary 软删除动画历史版本
// @Description 软删除指定历史版本（当前生效版本不可删除，删除后不再出现在版本列表中）
// @Tags animation
// @Accept json
// @Produce json
// @Param sceneId path int true "场景ID"
// @Param animationId path int true "动画ID"
// @Param version path int true "版本号"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/scenes/{sceneId}/animations/{animationId}/versions/{version} [delete]
func (h *AnimationHandler) DeleteVersion(c *gin.Context) {
	sceneId := c.Param("sceneId")
	animationId := c.Param("animationId")
	versionStr := c.Param("version")
	targetVersion, err := strconv.Atoi(versionStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid version number",
		})
		return
	}

	db := database.GetDB()

	// 检查场景是否存在
	var scene models.Scene
	if err := db.First(&scene, sceneId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Scene not found",
		})
		return
	}

	// 检查动画是否存在
	var animation models.SceneAnimation
	if err := db.Where("id = ? AND scene_id = ?", animationId, scene.ID).First(&animation).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Animation not found",
		})
		return
	}

	// 获取目标版本
	var version models.SceneAnimationVersion
	if err := db.Where("scene_animation_id = ? AND version = ?",
		animation.ID, targetVersion).First(&version).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Version not found",
		})
		return
	}

	// 当前生效版本不可删除，避免动画记录指向被删除的版本
	if animation.AnimationVersion == version.Version {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Cannot delete the current version",
		})
		return
	}

	// 模型带 gorm.DeletedAt，这里执行的是软删除
	if err := db.Delete(&version).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete version",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Version deleted successfully",
	})
}
