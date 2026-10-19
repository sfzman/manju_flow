package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"manju-flow/internal/models"
)

func TestParseImportedChapterDraftAcceptsJSONCodeFence(t *testing.T) {
	raw := "```json\n" + `{
  "title": "第三集",
  "synopsis": "男主发现追杀者的线索并开始谋划反击。",
  "scenes": [
    {
      "description": "【矿场、室外、白天】【回顾】男主手捧宝石。",
      "cameraMovement": "",
      "dialogue": "旁白/男主OS：被抓到矿场打黑工。",
      "transitionEffect": "回顾"
    }
  ]
}` + "\n```"

	draft, err := parseImportedChapterDraft(raw)
	if err != nil {
		t.Fatalf("parseImportedChapterDraft() error = %v", err)
	}
	if draft.Title != "第三集" || len(draft.Scenes) != 1 {
		t.Fatalf("unexpected draft: %#v", draft)
	}
}

func TestParseImportedChapterDraftRejectsMissingSceneDescription(t *testing.T) {
	raw := `{"title":"第三集","synopsis":"故事梗概","scenes":[{"description":""}]}`
	if _, err := parseImportedChapterDraft(raw); err == nil {
		t.Fatal("expected missing scene description to be rejected")
	}
}

func TestChapterImportFailureMessageAppendsErrorDetail(t *testing.T) {
	base := "AI 分析脚本失败，请重新导入"
	if got := chapterImportFailureMessage(base, nil); got != base {
		t.Fatalf("chapterImportFailureMessage() with nil error = %q, want %q", got, base)
	}

	got := chapterImportFailureMessage(base, errors.New("ark responses request failed: 401 unauthorized"))
	if !strings.Contains(got, base) || !strings.Contains(got, "401 unauthorized") {
		t.Fatalf("chapterImportFailureMessage() = %q, want base message with error detail", got)
	}

	multiline := chapterImportFailureMessage(base, errors.New("decode failed:\n\tunexpected\twhitespace"))
	if strings.Contains(multiline, "\n") || strings.Contains(multiline, "\t") {
		t.Fatalf("chapterImportFailureMessage() should collapse whitespace, got %q", multiline)
	}

	long := chapterImportFailureMessage(base, errors.New(strings.Repeat("x", 2000)))
	if runes := []rune(long); len(runes) > 500 {
		t.Fatalf("chapterImportFailureMessage() length = %d runes, want <= 500", len(runes))
	}
}

func TestChapterImportLeaseCoversAITimeout(t *testing.T) {
	// 未加载配置时使用内置默认值，租约必须覆盖默认 AI 超时
	if got := chapterImportAITimeout(); got != defaultChapterImportAITimeout {
		t.Fatalf("chapterImportAITimeout() with nil config = %v, want %v", got, defaultChapterImportAITimeout)
	}
	lease := chapterImportLeaseDuration()
	if lease <= defaultChapterImportAITimeout {
		t.Fatalf("chapterImportLeaseDuration() = %v, must exceed AI timeout %v", lease, defaultChapterImportAITimeout)
	}
}

func TestChapterImportPromptPreservesNumberedShots(t *testing.T) {
	prompt := buildChapterImportSystemPrompt()
	for _, expected := range []string{"每个编号都生成一个 scene", "不得合并、遗漏或改变顺序", "只输出一个合法 JSON 对象"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt is missing %q", expected)
		}
	}
}

func TestChapterImportPromptPreservesOriginalFormatting(t *testing.T) {
	prompt := buildChapterImportSystemPrompt()
	for _, expected := range []string{
		"逐字摘录原文",
		"换行必须用 \\n 保留",
		"【】",
		"每行单独一行并用 \\n 分隔",
		"一行都不能遗漏",
		"不得抄进 description",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("system prompt is missing %q", expected)
		}
	}
}

func TestChapterImportTaskJSONHidesPersistedPayload(t *testing.T) {
	task := models.ChapterImportTask{
		Status:          models.ChapterImportTaskStatusAnalyzing,
		ScriptContent:   "private script",
		AIResponse:      `{"title":"private response"}`,
		ProcessingToken: "private token",
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, secret := range []string{"private script", "private response", "private token"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("task API response leaked %q", secret)
		}
	}
}

func TestImportChapterTaskDataSetsSceneBookID(t *testing.T) {
	db := setupSceneAssetTestDB(t)
	if err := db.AutoMigrate(&models.ChapterImportTask{}); err != nil {
		t.Fatalf("migrate chapter import task schema: %v", err)
	}
	bookID, _ := createTestBookAndChapter(t, db, "Import Book")
	task := models.ChapterImportTask{
		BookID:           bookID,
		Status:           models.ChapterImportTaskStatusImporting,
		OriginalFilename: "chapter.txt",
		ScriptContent:    "script",
		Model:            "test-model",
		ProcessingToken:  "processing-token",
		CreatedBy:        1,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create import task: %v", err)
	}

	err := importChapterTaskData(db, &task, importedChapterDraft{
		Title:    "Imported Chapter",
		Synopsis: "Imported synopsis",
		Scenes: []importedSceneDraft{
			{Description: "First scene"},
			{Description: "Second scene"},
		},
	})
	if err != nil {
		t.Fatalf("importChapterTaskData() error = %v", err)
	}

	var scenes []models.Scene
	var updatedTask models.ChapterImportTask
	if err := db.First(&updatedTask, task.ID).Error; err != nil {
		t.Fatalf("load updated import task: %v", err)
	}
	if updatedTask.OutputChapterID == nil {
		t.Fatal("import task output chapter ID is nil")
	}
	if err := db.Where("chapter_id = ?", *updatedTask.OutputChapterID).Order("`index` ASC").Find(&scenes).Error; err != nil {
		t.Fatalf("load imported scenes: %v", err)
	}
	if len(scenes) != 2 {
		t.Fatalf("imported scene count = %d, want 2", len(scenes))
	}
	for _, scene := range scenes {
		if scene.BookID != bookID {
			t.Fatalf("imported scene %d book ID = %d, want %d", scene.ID, scene.BookID, bookID)
		}
	}
}
