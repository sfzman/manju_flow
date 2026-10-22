package models

import (
	"time"

	"gorm.io/gorm"
)

// AdaptationStatus 改编状态
type AdaptationStatus string

const (
	AdaptationStatusNone       AdaptationStatus = "NONE"        // 未开始改编（历史值，新建不再使用）
	AdaptationStatusInProgress AdaptationStatus = "IN_PROGRESS" // 创作中（默认）
	AdaptationStatusCompleted  AdaptationStatus = "COMPLETED"   // 已完成
	AdaptationStatusArchived   AdaptationStatus = "ARCHIVED"    // 已归档
)

// Book 作品模型。
type Book struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	Title          string `gorm:"size:255;not null" json:"title"`
	Author         string `gorm:"size:100;not null" json:"author"`
	Cover          string `gorm:"size:500" json:"cover"`          // 竖版封面 URL（9:16）
	CoverLandscape string `gorm:"size:500" json:"coverLandscape"` // 横版封面 URL（16:9）
	// Deprecated: 仅保留数据库列以兼容历史数据，业务不得再依赖该字段。
	Type                string           `gorm:"size:20;not null;default:'NOVEL';comment:Deprecated - work category is no longer used" json:"-"`
	Description         string           `gorm:"type:text" json:"description"`
	AdaptationStatus    AdaptationStatus `gorm:"size:20;default:'NONE'" json:"adaptationStatus"`
	AdaptedBy           string           `gorm:"size:100" json:"adaptedBy"` // 正在改编此作品的编剧
	ChapterCount        int              `gorm:"default:0" json:"chapterCount"`
	Outline             string           `gorm:"type:text" json:"outline"`  // 大纲（纯文本）
	ArtStyle            string           `gorm:"type:text" json:"artStyle"` // 画风提示词（与作品一对一，可选）
	OriginalTextKey     string           `gorm:"size:500" json:"originalTextKey"`
	OriginalTextPreview string           `gorm:"type:text" json:"originalTextPreview"`
	CreatedAt           time.Time        `json:"createdAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
	DeletedAt           gorm.DeletedAt   `gorm:"index" json:"-"`
	IsFavorite          bool             `gorm:"-" json:"isFavorite"`
}

// TableName 指定表名
func (Book) TableName() string {
	return "books"
}

// BookListResponse 列表响应
type BookListResponse struct {
	Total int64  `json:"total"`
	Page  int    `json:"page"`
	Size  int    `json:"size"`
	Data  []Book `json:"data"`
}

// CreateBookRequest 创建书籍请求
type CreateBookRequest struct {
	Title               string `json:"title" binding:"required"`
	Author              string `json:"author" binding:"required"`
	Cover               string `json:"cover"`
	CoverLandscape      string `json:"coverLandscape"`
	Description         string `json:"description"`
	Outline             string `json:"outline"`
	ArtStyle            string `json:"artStyle"`
	OriginalTextKey     string `json:"originalTextKey"`
	OriginalTextPreview string `json:"originalTextPreview"`
}

// UpdateOutlineRequest 更新大纲请求
type UpdateOutlineRequest struct {
	Outline string `json:"outline"`
}

// UpdateArtStyleRequest 更新画风提示词请求
type UpdateArtStyleRequest struct {
	ArtStyle string `json:"artStyle"`
}
