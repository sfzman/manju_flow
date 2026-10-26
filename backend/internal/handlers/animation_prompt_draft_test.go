package handlers

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"manju-flow/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var animationPromptDraftTestDBSequence uint64

func setupAnimationPromptDraftTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dbName := fmt.Sprintf("animation_prompt_draft_test_%d", atomic.AddUint64(&animationPromptDraftTestDBSequence, 1))
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName)), &gorm.Config{
		TranslateError:                           true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}

	if err := db.AutoMigrate(&models.Character{}, &models.Scene{}, &models.SceneAudio{}); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	return db
}

func TestListAnimationPromptDraftAudioRoles(t *testing.T) {
	db := setupAnimationPromptDraftTestDB(t)
	scene := models.Scene{ID: 1, BookID: 10, ChapterID: 100, Index: 1}
	if err := db.Create(&scene).Error; err != nil {
		t.Fatalf("create scene: %v", err)
	}

	tracks := []models.SceneAudio{
		{SceneID: 1, Role: "连瑟", Index: 1},
		{SceneID: 1, Role: "旁白", Index: 2},
		{SceneID: 1, Role: "连瑟", Index: 3},
		{SceneID: 1, Role: "   ", Index: 4},
	}
	for _, track := range tracks {
		if err := db.Create(&track).Error; err != nil {
			t.Fatalf("create scene audio: %v", err)
		}
	}

	got := listAnimationPromptDraftAudioRoles(db, []uint{1})
	if len(got) != 2 || got[0] != "连瑟" || got[1] != "旁白" {
		t.Errorf("listAnimationPromptDraftAudioRoles() = %v, want [连瑟 旁白]", got)
	}
}

func TestLoadAnimationPromptDraftRosterCharacters(t *testing.T) {
	db := setupAnimationPromptDraftTestDB(t)
	scene := models.Scene{ID: 1, BookID: 10, ChapterID: 100, Index: 1}
	if err := db.Create(&scene).Error; err != nil {
		t.Fatalf("create scene: %v", err)
	}
	seedCharacter := func(id uint, name string, index float64) {
		character := models.Character{ID: id, BookID: 10, Name: name, Index: index}
		if err := db.Create(&character).Error; err != nil {
			t.Fatalf("create character %s: %v", name, err)
		}
	}
	seedCharacter(1, "连瑟", 2)
	seedCharacter(2, "阿羽", 1)
	seedCharacter(3, "路人", 3)
	// 其他作品的 character 不进入名单
	if err := db.Create(&models.Character{ID: 4, BookID: 99, Name: "外传角色", Index: 1}).Error; err != nil {
		t.Fatalf("create other book character: %v", err)
	}

	got := loadAnimationPromptDraftRosterCharacters(db, scene)
	if len(got) != 3 {
		t.Fatalf("loadAnimationPromptDraftRosterCharacters() got %d characters, want 3", len(got))
	}
	if got[0].Name != "阿羽" || got[1].Name != "连瑟" || got[2].Name != "路人" {
		t.Errorf("roster order = [%s, %s, %s], want [阿羽, 连瑟, 路人]", got[0].Name, got[1].Name, got[2].Name)
	}
}

func TestExtractAnimationPromptDraftCharacterNames(t *testing.T) {
	draft := "镜头 1：连瑟在广场啃面包。\n镜头 2：弟子侧目。\n【出场人物】连瑟、归一宗弟子\n【说话人物】连瑟"
	cleaned, appearing, speaking := extractAnimationPromptDraftCharacterNames(draft)
	if cleaned != "镜头 1：连瑟在广场啃面包。\n镜头 2：弟子侧目。" {
		t.Errorf("cleaned draft = %q, want marker lines stripped", cleaned)
	}
	if len(appearing) != 2 || appearing[0] != "连瑟" || appearing[1] != "归一宗弟子" {
		t.Errorf("appearing = %v, want [连瑟 归一宗弟子]", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "连瑟" {
		t.Errorf("speaking = %v, want [连瑟]", speaking)
	}

	// 画外音：说话但不出场，两份名单相互独立
	voiceover := "镜头 1：空镜。\n【出场人物】无\n【说话人物】神秘人"
	cleaned, appearing, speaking = extractAnimationPromptDraftCharacterNames(voiceover)
	if len(appearing) != 0 {
		t.Errorf("appearing = %v, want empty for 无", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "神秘人" {
		t.Errorf("speaking = %v, want [神秘人]", speaking)
	}
	if strings.Contains(cleaned, "神秘人") {
		t.Errorf("speaking list should be stripped from draft, got %q", cleaned)
	}

	// 标注为无出场人物
	_, appearing, _ = extractAnimationPromptDraftCharacterNames("镜头 1：空镜。\n【出场人物】无")
	if len(appearing) != 0 {
		t.Errorf("appearing = %v, want empty for 无", appearing)
	}
	_, appearing, _ = extractAnimationPromptDraftCharacterNames("镜头 1：空镜。\n【出场人物】无。")
	if len(appearing) != 0 {
		t.Errorf("appearing = %v, want empty for 无。", appearing)
	}

	// 未标注名单行：草稿保持原样、名单为空
	unchanged := "镜头 1：纯画面。"
	cleaned, appearing, speaking = extractAnimationPromptDraftCharacterNames(unchanged)
	if cleaned != unchanged || len(appearing) != 0 || len(speaking) != 0 {
		t.Errorf("draft without marker should stay unchanged, got %q / %v / %v", cleaned, appearing, speaking)
	}

	// 标记行变体：无括号带冒号、英文逗号、重复标注去重
	variant := "镜头 1：开场。\n出场人物：连瑟, 阿羽\n【出场人物】连瑟、阿羽\n说话人物：连瑟"
	_, appearing, speaking = extractAnimationPromptDraftCharacterNames(variant)
	if len(appearing) != 2 || appearing[0] != "连瑟" || appearing[1] != "阿羽" {
		t.Errorf("variant appearing = %v, want [连瑟 阿羽]", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "连瑟" {
		t.Errorf("variant speaking = %v, want [连瑟]", speaking)
	}

	// 名单写在标记行的下一行
	nextLine := "镜头 1：开场。\n【出场人物】\n连瑟、阿羽\n【说话人物】\n阿羽"
	cleaned, appearing, speaking = extractAnimationPromptDraftCharacterNames(nextLine)
	if len(appearing) != 2 || appearing[0] != "连瑟" || appearing[1] != "阿羽" {
		t.Errorf("next-line appearing = %v, want [连瑟 阿羽]", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "阿羽" {
		t.Errorf("next-line speaking = %v, want [阿羽]", speaking)
	}
	if strings.Contains(cleaned, "连瑟、阿羽") || strings.Contains(cleaned, "阿羽") {
		t.Errorf("next-line list should be stripped from draft, got %q", cleaned)
	}

	// LLM 附带括号说明时保留完整 token（括号内容可能是名字的一部分，如 林凡(成年)），
	// 全角括号归一为半角；说明性括号由 matchAnimationPromptDraftCharacters 分层剥掉
	annotated := "镜头 1：开场。\n【出场人物】连瑟（女主）、阿羽\n【说话人物】阿羽（旁白）"
	_, appearing, speaking = extractAnimationPromptDraftCharacterNames(annotated)
	if len(appearing) != 2 || appearing[0] != "连瑟(女主)" || appearing[1] != "阿羽" {
		t.Errorf("annotated appearing = %v, want [连瑟(女主) 阿羽]", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "阿羽(旁白)" {
		t.Errorf("annotated speaking = %v, want [阿羽(旁白)]", speaking)
	}
}

func TestMergeAnimationPromptDraftSpeakingNames(t *testing.T) {
	// 音频轨道角色兜底并入说话名单：保序、去重
	merged := mergeAnimationPromptDraftSpeakingNames([]string{"连瑟", "阿羽"}, []string{"阿羽", "旁白", "李雷"})
	if len(merged) != 4 || merged[0] != "连瑟" || merged[1] != "阿羽" || merged[2] != "旁白" || merged[3] != "李雷" {
		t.Errorf("merged = %v, want [连瑟 阿羽 旁白 李雷]", merged)
	}
	if merged = mergeAnimationPromptDraftSpeakingNames(nil, nil); len(merged) != 0 {
		t.Errorf("empty inputs should merge to empty, got %v", merged)
	}
}

func TestMatchAnimationPromptDraftCharacters(t *testing.T) {
	roster := []models.Character{
		{ID: 1, Name: " 连瑟 ", CoreFeatures: "黑长发红瞳"},
		{ID: 2, Name: "连瑟"},
		{ID: 3, Name: "归一宗弟子"},
		{ID: 4, Name: "  "},
	}

	got := matchAnimationPromptDraftCharacters(roster, []string{"归一宗弟子", "连瑟", "不存在"})
	if len(got) != 2 {
		t.Fatalf("matched %d characters, want 2", len(got))
	}
	if got[0].ID != 3 || got[1].ID != 1 {
		t.Errorf("matched IDs = [%d, %d], want [3, 1] (LLM 顺序、重名取排位靠前)", got[0].ID, got[1].ID)
	}
}

func TestAnimationPromptDraftSplitRosterNames(t *testing.T) {
	cases := []struct {
		desc    string
		content string
		want    []string
	}{
		{desc: "顿号分隔", content: "连瑟、归一宗弟子", want: []string{"连瑟", "归一宗弟子"}},
		{desc: "逗号/分号/斜杠/空白分隔", content: "连瑟, 阿羽；旁白/神秘人", want: []string{"连瑟", "阿羽", "旁白", "神秘人"}},
		{desc: "带括号的名字保留完整 token", content: "林凡(成年)、苏寒月(人妻)", want: []string{"林凡(成年)", "苏寒月(人妻)"}},
		{desc: "全角括号归一为半角", content: "林凡（成年）、苏寒月（人妻）", want: []string{"林凡(成年)", "苏寒月(人妻)"}},
		{desc: "引号剥掉、括号保留", content: `“连瑟（女主）”、“阿羽”`, want: []string{"连瑟(女主)", "阿羽"}},
		{desc: "尾部说明括号也保留（由匹配层解析）", content: "林凡(成年)（男主）、阿羽", want: []string{"林凡(成年)(男主)", "阿羽"}},
		{desc: "空内容", content: "", want: nil},
		{desc: "只剩分隔符", content: "、，,；;/ \t", want: nil},
	}
	for _, tc := range cases {
		got := animationPromptDraftSplitRosterNames(tc.content)
		if len(got) != len(tc.want) {
			t.Errorf("%s: animationPromptDraftSplitRosterNames(%q) = %v, want %v", tc.desc, tc.content, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: animationPromptDraftSplitRosterNames(%q)[%d] = %q, want %q", tc.desc, tc.content, i, got[i], tc.want[i])
			}
		}
	}
}

func TestMatchAnimationPromptDraftCharactersLayered(t *testing.T) {
	rosterOf := func(names ...string) []models.Character {
		roster := make([]models.Character, 0, len(names))
		for i, name := range names {
			roster = append(roster, models.Character{ID: uint(i + 1), Name: name, Index: float64(i)})
		}
		return roster
	}
	cases := []struct {
		desc   string
		roster []models.Character
		names  []string
		want   []string
	}{
		{
			desc:   "名字带括号且与名单完全一致 → 精确命中",
			roster: rosterOf("林凡(成年)"),
			names:  []string{"林凡(成年)"},
			want:   []string{"林凡(成年)"},
		},
		{
			desc:   "全角括号归一后命中",
			roster: rosterOf("林凡(成年)"),
			names:  []string{"林凡（成年）"},
			want:   []string{"林凡(成年)"},
		},
		{
			desc:   "根名在名单中变体唯一 → 兜底命中",
			roster: rosterOf("林凡(成年)"),
			names:  []string{"林凡"},
			want:   []string{"林凡(成年)"},
		},
		{
			desc:   "根名有多个变体 → 不瞎猜、宁可不挂",
			roster: rosterOf("林凡(少年)", "林凡(成年)", "林凡(病房)"),
			names:  []string{"林凡"},
			want:   nil,
		},
		{
			desc:   "带尾部说明括号 → 剥掉说明后命中",
			roster: rosterOf("林凡(成年)"),
			names:  []string{"林凡(成年)（男主）"},
			want:   []string{"林凡(成年)"},
		},
		{
			desc:   "纯说明括号剥掉后命中名单裸名",
			roster: rosterOf("连瑟", "阿羽"),
			names:  []string{"连瑟(女主)", "阿羽(旁白)"},
			want:   []string{"连瑟", "阿羽"},
		},
		{
			desc:   "不带括号的普通名字 → 保持原行为命中（回归保护）",
			roster: rosterOf("礼部侍郎"),
			names:  []string{"礼部侍郎"},
			want:   []string{"礼部侍郎"},
		},
		{
			desc:   "名单外人物忽略、保持 LLM 顺序",
			roster: rosterOf("林凡(成年)", "苏寒月(人妻)"),
			names:  animationPromptDraftSplitRosterNames("林凡(成年)、苏寒月（人妻）、不存在的人"),
			want:   []string{"林凡(成年)", "苏寒月(人妻)"},
		},
		{
			desc:   "多个写法解析到同一角色 → 去重只挂一次",
			roster: rosterOf("林凡(成年)"),
			names:  []string{"林凡(成年)（男主）", "林凡"},
			want:   []string{"林凡(成年)"},
		},
	}
	for _, tc := range cases {
		got := matchAnimationPromptDraftCharacters(tc.roster, tc.names)
		if len(got) != len(tc.want) {
			t.Errorf("%s: matched %d characters (%v), want %v", tc.desc, len(got), characterNames(got), tc.want)
			continue
		}
		for i := range got {
			if got[i].Name != tc.want[i] {
				t.Errorf("%s: matched[%d] = %q, want %q", tc.desc, i, got[i].Name, tc.want[i])
			}
		}
	}
}

func TestExtractAnimationPromptDraftCharacterNamesParenVariants(t *testing.T) {
	// 名单行占位"无"的各种写法 → 名单为空
	for _, content := range []string{"无", "（无）", "(无)", "None", "无。"} {
		_, appearing, speaking := extractAnimationPromptDraftCharacterNames("镜头 1：空镜。\n【出场人物】" + content + "\n【说话人物】" + content)
		if len(appearing) != 0 || len(speaking) != 0 {
			t.Errorf("placeholder %q should produce empty lists, got appearing=%v speaking=%v", content, appearing, speaking)
		}
	}

	// 带括号变体名整链路：切分保留括号并归一，供匹配层精确命中
	draft := "镜头 1：病房走廊。\n【出场人物】林凡(成年)、苏寒月（人妻）、不存在的人\n【说话人物】林凡(病房)"
	_, appearing, speaking := extractAnimationPromptDraftCharacterNames(draft)
	if len(appearing) != 3 || appearing[0] != "林凡(成年)" || appearing[1] != "苏寒月(人妻)" || appearing[2] != "不存在的人" {
		t.Errorf("appearing = %v, want [林凡(成年) 苏寒月(人妻) 不存在的人]", appearing)
	}
	if len(speaking) != 1 || speaking[0] != "林凡(病房)" {
		t.Errorf("speaking = %v, want [林凡(病房)]", speaking)
	}
}

// characterNames 提取人物名列表，便于断言信息展示
func characterNames(characters []models.Character) []string {
	names := make([]string, 0, len(characters))
	for _, character := range characters {
		names = append(names, character.Name)
	}
	return names
}

func TestBuildAnimationPromptDraftCharacterReferences(t *testing.T) {
	// 出场且说话 → 参考图 + 音色
	slotPriorityCharacter := models.Character{
		ID: 1, Name: "连瑟", CoreFeatures: "黑长发红瞳",
		ReferenceImageUrl: "uploads/lianse-sanshi.png", HalfBodyFrontImageUrl: "uploads/lianse-half.png",
		VoiceAudioUrl: "uploads/lianse-voice.mp3",
	}
	references := buildAnimationPromptDraftCharacterReferences(
		[]models.Character{slotPriorityCharacter}, []models.Character{slotPriorityCharacter},
	)
	if len(references) != 1 {
		t.Fatalf("got %d references, want 1", len(references))
	}
	reference := references[0]
	if reference.ImageKey != "uploads/lianse-sanshi.png" {
		t.Errorf("ImageKey = %q, want 三视图 to take priority over 半身正面", reference.ImageKey)
	}
	if reference.ImageSlot != "referenceImageUrl" || reference.ImageSlotLabel != "三视图" {
		t.Errorf("image slot = %q/%q, want referenceImageUrl/三视图", reference.ImageSlot, reference.ImageSlotLabel)
	}
	if reference.VoiceAudioKey != "uploads/lianse-voice.mp3" {
		t.Errorf("VoiceAudioKey = %q, want uploads/lianse-voice.mp3", reference.VoiceAudioKey)
	}
	if !reference.Appears || !reference.Speaking {
		t.Errorf("flags = appears:%v speaking:%v, want both true", reference.Appears, reference.Speaking)
	}

	// 出场但无对白 → 只附加参考图，不附加音色
	appearingOnly := models.Character{ID: 2, Name: "阿羽", HalfBodyFrontImageUrl: "uploads/ayu-half.png", VoiceAudioUrl: "uploads/ayu-voice.mp3"}
	references = buildAnimationPromptDraftCharacterReferences([]models.Character{appearingOnly}, nil)
	if len(references) != 1 {
		t.Fatalf("got %d references, want 1", len(references))
	}
	if references[0].ImageSlot != "halfBodyFrontImageUrl" || references[0].ImageKey != "uploads/ayu-half.png" {
		t.Fatalf("fallback slot = %+v, want halfBodyFrontImageUrl", references[0])
	}
	if references[0].VoiceAudioKey != "" {
		t.Errorf("VoiceAudioKey = %q, want empty for 出场但无对白", references[0].VoiceAudioKey)
	}
	if !references[0].Appears || references[0].Speaking {
		t.Errorf("flags = appears:%v speaking:%v, want true/false", references[0].Appears, references[0].Speaking)
	}

	// 画外音：只说话不出场 → 只附加音色，不附加参考图
	voiceover := models.Character{ID: 3, Name: "神秘人", VoiceAudioUrl: "uploads/mystery-voice.mp3"}
	references = buildAnimationPromptDraftCharacterReferences(nil, []models.Character{voiceover})
	if len(references) != 1 {
		t.Fatalf("got %d references, want 1", len(references))
	}
	if references[0].VoiceAudioKey != "uploads/mystery-voice.mp3" {
		t.Errorf("VoiceAudioKey = %q, want uploads/mystery-voice.mp3", references[0].VoiceAudioKey)
	}
	if references[0].ImageKey != "" {
		t.Errorf("ImageKey = %q, want empty for 画外音（未出场）", references[0].ImageKey)
	}
	if references[0].Appears || !references[0].Speaking {
		t.Errorf("flags = appears:%v speaking:%v, want false/true", references[0].Appears, references[0].Speaking)
	}

	// 未配置任何参考的人物被过滤：出场但无图无音色、说话但无音色
	unconfigured := models.Character{ID: 4, Name: "路人甲"}
	unvoiced := models.Character{ID: 5, Name: "未配音旁白"}
	if references = buildAnimationPromptDraftCharacterReferences([]models.Character{unconfigured}, []models.Character{unvoiced}); len(references) != 0 {
		t.Errorf("unconfigured characters should be skipped, got %+v", references)
	}

	// 出场超过上限时按出场顺序截断
	var many []models.Character
	for i := 0; i < maxAnimationDraftCharacterReferences+2; i++ {
		many = append(many, models.Character{
			ID: uint(i + 1), Name: fmt.Sprintf("角色%d", i), Index: float64(i),
			ReferenceImageUrl: fmt.Sprintf("uploads/char-%d.png", i),
		})
	}
	if references = buildAnimationPromptDraftCharacterReferences(many, nil); len(references) != maxAnimationDraftCharacterReferences {
		t.Errorf("got %d references, want capped at %d", len(references), maxAnimationDraftCharacterReferences)
	}

	// 说话人数超过音色上限时按说话顺序截断（仅说话的人物不挤占出场参考位）
	var speakers []models.Character
	for i := 0; i < maxAnimationDraftCharacterVoiceReferences+2; i++ {
		speakers = append(speakers, models.Character{
			ID: uint(i + 1), Name: fmt.Sprintf("画外音%d", i),
			VoiceAudioUrl: fmt.Sprintf("uploads/voice-%d.mp3", i),
		})
	}
	if references = buildAnimationPromptDraftCharacterReferences(nil, speakers); len(references) != maxAnimationDraftCharacterVoiceReferences {
		t.Errorf("got %d references, want voice capped at %d", len(references), maxAnimationDraftCharacterVoiceReferences)
	}
}

func TestBuildAnimationPromptDraftCharacterContext(t *testing.T) {
	if context := buildAnimationPromptDraftCharacterContext(nil, nil); context != "" {
		t.Errorf("empty roster should produce empty context, got %q", context)
	}

	context := buildAnimationPromptDraftCharacterContext([]models.Character{
		{Name: " 连瑟 ", CoreFeatures: " 黑长发红瞳少女 "},
		{Name: "阿羽", Description: "白发少年剑客，背着长剑"},
	}, []string{"连瑟", "旁白"})
	for _, want := range []string{"【人物人设名单", "- 连瑟：黑长发红瞳少女", "- 阿羽：白发少年剑客，背着长剑", "连瑟、旁白", "必须列入草稿最后的【说话人物】名单"} {
		if !strings.Contains(context, want) {
			t.Errorf("character context missing %q:\n%s", want, context)
		}
	}

	withoutRoles := buildAnimationPromptDraftCharacterContext([]models.Character{{Name: "连瑟"}}, nil)
	if strings.Contains(withoutRoles, "音频轨道角色") {
		t.Error("empty audio roles should not add a section")
	}
}

func TestBuildAnimationPromptDraftSystemPromptRequiresCharacterLine(t *testing.T) {
	prompt := buildAnimationPromptDraftSystemPrompt()
	for _, want := range []string{
		"人物判定", "【出场人物】人物A、人物B", "【说话人物】人物A、人物C",
		"提及", "不算出场", "画外音", "相互独立", "最多 4 位",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestBuildAnimationPromptDraftUserPromptIncludesCharacterContext(t *testing.T) {
	prompt := buildAnimationPromptDraftUserPrompt(
		[]animationPromptDraftScene{{
			Position: 1,
			Scene:    models.Scene{Description: "连瑟在广场啃面包"},
		}},
		"",
		"【人物人设名单｜来自大纲人设模块】\n- 连瑟：黑长发红瞳少女",
	)
	characterIndex := strings.Index(prompt, "【人物人设名单")
	sceneInfoIndex := strings.Index(prompt, "【分镜文字信息")
	if sceneInfoIndex < 0 || characterIndex < 0 || characterIndex < sceneInfoIndex {
		t.Errorf("character context should follow scene text section, got sceneInfo=%d character=%d", sceneInfoIndex, characterIndex)
	}
	if !strings.Contains(prompt, "连瑟在广场啃面包") {
		t.Error("user prompt should keep scene description")
	}

	withoutCharacters := buildAnimationPromptDraftUserPrompt(
		[]animationPromptDraftScene{{Position: 1, Scene: models.Scene{Description: "连瑟在广场啃面包"}}},
		"",
		"",
	)
	if strings.Contains(withoutCharacters, "人物人设名单") {
		t.Error("empty character context should not add a section")
	}
}
