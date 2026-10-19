package handlers

import (
	"archive/zip"
	"bytes"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"manju-flow/internal/database"
	"manju-flow/internal/models"
	"manju-flow/internal/oss"

	"github.com/gin-gonic/gin"
)

// VideoHandler 视频处理器
type VideoHandler struct{}

// NewVideoHandler 创建视频处理器
func NewVideoHandler() *VideoHandler {
	return &VideoHandler{}
}

// GetInfo 获取章节视频信息
// @Summary 获取章节视频信息
// @Description 获取指定章节的交付视频信息
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Success 200 {object} models.ChapterVideoResponse
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video [get]
func (h *VideoHandler) GetInfo(c *gin.Context) {
	chapterId := c.Param("chapterId")

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		// 如果不存在，返回空对象但不报错
		c.JSON(http.StatusOK, models.ChapterVideoResponse{
			ChapterVideo: models.ChapterVideo{
				ChapterID: chapter.ID,
				Status:    models.VideoStatusPending,
			},
			VersionCount: 0,
		})
		return
	}

	// 获取版本数量
	var versionCount int64
	db.Model(&models.ChapterVideoVersion{}).Where("chapter_video_id = ?", video.ID).Count(&versionCount)

	c.JSON(http.StatusOK, models.ChapterVideoResponse{
		ChapterVideo: video,
		VersionCount: int(versionCount),
	})
}

// Upload 上传/更新章节视频
// @Summary 上传章节视频
// @Description 为章节上传新版本视频
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param video body models.UploadVideoRequest true "视频信息"
// @Success 200 {object} models.ChapterVideoVersion
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video [put]
func (h *VideoHandler) Upload(c *gin.Context) {
	chapterId := c.Param("chapterId")
	chapterIdUint, err := strconv.ParseUint(chapterId, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid chapter ID",
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

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	var req models.UploadVideoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 开始事务
	tx := db.Begin()

	// 查找或创建章节视频记录
	var video models.ChapterVideo
	result := tx.Where("chapter_id = ?", chapter.ID).First(&video)
	isNew := result.Error != nil

	if isNew {
		// 创建新记录
		video = models.ChapterVideo{
			ChapterID: uint(chapterIdUint),
		}
		if err := tx.Create(&video).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to create video record",
			})
			return
		}
	}

	// 获取当前最大版本号（含软删除版本，避免版本号复用）
	var maxVersion int
	tx.Unscoped().Model(&models.ChapterVideoVersion{}).
		Where("chapter_video_id = ?", video.ID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&maxVersion)

	newVersion := maxVersion + 1

	// 创建新版本记录
	version := models.ChapterVideoVersion{
		ChapterVideoID: video.ID,
		VideoUrl:       req.VideoUrl,
		PreviewUrl:     req.PreviewUrl,
		Version:        newVersion,
		Duration:       req.Duration,
		FileSize:       req.FileSize,
		PreviewSize:    req.PreviewSize,
		Width:          req.Width,
		Height:         req.Height,
		Remark:         req.Remark,
		CreatedBy:      userId.(uint),
	}

	if err := tx.Create(&version).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create version",
		})
		return
	}

	// 确定状态：如果有预览版则就绪，否则待处理
	status := models.VideoStatusPending
	if req.PreviewUrl != "" {
		status = models.VideoStatusReady
	}

	// 更新章节视频的当前信息
	updates := map[string]interface{}{
		"video_url":       req.VideoUrl,
		"preview_url":     req.PreviewUrl,
		"video_version":   newVersion,
		"status":          status,
		"duration":        req.Duration,
		"file_size":       req.FileSize,
		"preview_size":    req.PreviewSize,
		"width":           req.Width,
		"height":          req.Height,
		"format":          req.Format,
		"codec":           req.Codec,
		"bitrate":         req.Bitrate,
		"preview_bitrate": req.PreviewBitrate,
	}

	if err := tx.Model(&video).Updates(updates).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update video",
		})
		return
	}

	tx.Commit()

	c.JSON(http.StatusOK, version)
}

func isZipUpload(filename string, contentType string) bool {
	if !strings.EqualFold(filepath.Ext(filename), ".zip") {
		return false
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return contentType == "" ||
		contentType == "application/zip" ||
		contentType == "application/x-zip-compressed" ||
		contentType == "application/octet-stream" ||
		contentType == "multipart/x-zip"
}

// UploadEditScript 上传/覆盖章节剪辑脚本 ZIP。脚本不保留历史版本，只覆盖当前章节记录。
// @Summary 上传剪辑脚本
// @Description 为章节上传剪辑脚本 ZIP，二次上传直接覆盖当前脚本
// @Tags video
// @Accept multipart/form-data
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param file formData file true "剪辑脚本 ZIP"
// @Success 200 {object} models.ChapterVideo
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/edit-script [put]
func (h *VideoHandler) UploadEditScript(c *gin.Context) {
	if !oss.IsConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "文件服务未配置",
		})
		return
	}

	chapterId := c.Param("chapterId")
	chapterIdUint, err := strconv.ParseUint(chapterId, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid chapter ID",
		})
		return
	}

	userId, exists := c.Get("userId")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "User not authenticated",
		})
		return
	}

	db := database.GetDB()

	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "请选择要上传的剪辑脚本 ZIP 文件",
		})
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if !isZipUpload(header.Filename, contentType) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "剪辑脚本只支持 zip 文件包",
		})
		return
	}
	if contentType == "" {
		contentType = "application/zip"
	}

	key, content, err := oss.GenerateKeyFromContent(file, header.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "处理剪辑脚本失败: " + err.Error(),
		})
		return
	}
	if _, err := zip.NewReader(bytes.NewReader(content), int64(len(content))); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "剪辑脚本只支持有效的 zip 文件包",
		})
		return
	}

	ossClient := oss.GetClient()
	ossExists, err := ossClient.Exists(key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "检查剪辑脚本失败: " + err.Error(),
		})
		return
	}
	if !ossExists {
		if err := ossClient.UploadBytes(key, content, contentType); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "剪辑脚本上传失败: " + err.Error(),
			})
			return
		}
	}

	now := time.Now()
	tx := db.Begin()

	var video models.ChapterVideo
	result := tx.Where("chapter_id = ?", chapter.ID).First(&video)
	if result.Error != nil {
		video = models.ChapterVideo{
			ChapterID: uint(chapterIdUint),
			Status:    models.VideoStatusPending,
		}
		if err := tx.Create(&video).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to create video record",
			})
			return
		}
	}

	updates := map[string]interface{}{
		"edit_script_url":         key,
		"edit_script_name":        header.Filename,
		"edit_script_size":        int64(len(content)),
		"edit_script_uploaded_at": &now,
	}
	if err := tx.Model(&video).Updates(updates).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update edit script",
		})
		return
	}

	fileRecord := &models.File{
		Key:          key,
		OriginalName: header.Filename,
		Size:         int64(len(content)),
		MimeType:     contentType,
		UploaderID:   userId.(uint),
		Visibility:   models.FileVisibilityPrivate,
	}
	if err := tx.Create(fileRecord).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "保存剪辑脚本记录失败: " + err.Error(),
		})
		return
	}

	tx.Commit()

	db.Where("chapter_id = ?", chapter.ID).First(&video)
	c.JSON(http.StatusOK, video)
}

// UploadPreview 上传预览版视频
// @Summary 上传预览版视频
// @Description 为当前版本上传压缩预览版
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param preview body models.UploadPreviewRequest true "预览版信息"
// @Success 200 {object} models.ChapterVideo
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/preview [put]
func (h *VideoHandler) UploadPreview(c *gin.Context) {
	chapterId := c.Param("chapterId")

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	// 获取章节视频
	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Video not found, please upload original video first",
		})
		return
	}

	var req models.UploadPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 开始事务
	tx := db.Begin()

	// 更新章节视频的预览信息
	updates := map[string]interface{}{
		"preview_url":     req.PreviewUrl,
		"preview_size":    req.PreviewSize,
		"preview_bitrate": req.PreviewBitrate,
		"status":          models.VideoStatusReady,
	}

	if err := tx.Model(&video).Updates(updates).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update preview",
		})
		return
	}

	// 同时更新当前版本记录的预览信息
	if video.VideoVersion > 0 {
		tx.Model(&models.ChapterVideoVersion{}).
			Where("chapter_video_id = ? AND version = ?", video.ID, video.VideoVersion).
			Updates(map[string]interface{}{
				"preview_url":  req.PreviewUrl,
				"preview_size": req.PreviewSize,
			})
	}

	tx.Commit()

	// 重新加载
	db.Where("chapter_id = ?", chapter.ID).First(&video)

	c.JSON(http.StatusOK, video)
}

// UpdateStatus 更新视频状态
// @Summary 更新视频状态
// @Description 更新章节视频处理状态
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param status body models.UpdateVideoStatusRequest true "状态信息"
// @Success 200 {object} models.ChapterVideo
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/status [put]
func (h *VideoHandler) UpdateStatus(c *gin.Context) {
	chapterId := c.Param("chapterId")

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Video not found",
		})
		return
	}

	var req models.UpdateVideoStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	if err := db.Model(&video).Update("status", req.Status).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update status",
		})
		return
	}

	// 重新加载
	db.Where("chapter_id = ?", chapter.ID).First(&video)

	c.JSON(http.StatusOK, video)
}

// ListVersions 获取视频版本历史
// @Summary 获取视频版本历史
// @Description 获取指定章节视频的所有版本历史
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Success 200 {object} models.ChapterVideoVersionListResponse
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/versions [get]
func (h *VideoHandler) ListVersions(c *gin.Context) {
	chapterId := c.Param("chapterId")

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	// 获取章节视频
	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		// 如果没有视频，返回空列表
		c.JSON(http.StatusOK, models.ChapterVideoVersionListResponse{
			Total: 0,
			Data:  []models.ChapterVideoVersion{},
		})
		return
	}

	var versions []models.ChapterVideoVersion
	if err := db.Where("chapter_video_id = ?", video.ID).
		Order("version DESC").
		Find(&versions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to fetch versions",
		})
		return
	}

	c.JSON(http.StatusOK, models.ChapterVideoVersionListResponse{
		Total: int64(len(versions)),
		Data:  versions,
	})
}

// Revert 回滚视频到指定版本
// @Summary 回滚视频
// @Description 将章节视频回滚到指定版本
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param version path int true "版本号"
// @Success 200 {object} models.ChapterVideo
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/revert/{version} [put]
func (h *VideoHandler) Revert(c *gin.Context) {
	chapterId := c.Param("chapterId")
	versionStr := c.Param("version")
	targetVersion, err := strconv.Atoi(versionStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid version number",
		})
		return
	}

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	// 获取章节视频
	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Video not found",
		})
		return
	}

	// 获取目标版本
	var version models.ChapterVideoVersion
	if err := db.Where("chapter_video_id = ? AND version = ?",
		video.ID, targetVersion).First(&version).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Version not found",
		})
		return
	}

	// 确定状态
	status := models.VideoStatusPending
	if version.PreviewUrl != "" {
		status = models.VideoStatusReady
	}

	// 更新章节视频的当前信息
	updates := map[string]interface{}{
		"video_url":     version.VideoUrl,
		"preview_url":   version.PreviewUrl,
		"video_version": version.Version,
		"status":        status,
		"duration":      version.Duration,
		"file_size":     version.FileSize,
		"preview_size":  version.PreviewSize,
		"width":         version.Width,
		"height":        version.Height,
	}

	if err := db.Model(&video).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to revert video",
		})
		return
	}

	// 重新加载
	db.Where("chapter_id = ?", chapter.ID).First(&video)

	c.JSON(http.StatusOK, video)
}

// DeleteVersion 软删除视频历史版本
// @Summary 软删除视频历史版本
// @Description 软删除指定历史版本（当前生效版本不可删除，删除后不再出现在版本列表中）
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Param version path int true "版本号"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video/versions/{version} [delete]
func (h *VideoHandler) DeleteVersion(c *gin.Context) {
	chapterId := c.Param("chapterId")
	versionStr := c.Param("version")
	targetVersion, err := strconv.Atoi(versionStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid version number",
		})
		return
	}

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	// 获取章节视频
	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Video not found",
		})
		return
	}

	// 获取目标版本
	var version models.ChapterVideoVersion
	if err := db.Where("chapter_video_id = ? AND version = ?",
		video.ID, targetVersion).First(&version).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Version not found",
		})
		return
	}

	// 当前生效版本不可删除，避免交付记录指向被删除的版本
	if video.VideoVersion == version.Version {
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

// Delete 删除章节视频（包括所有版本）
// @Summary 删除章节视频
// @Description 删除章节视频及其所有版本历史
// @Tags video
// @Accept json
// @Produce json
// @Param chapterId path int true "章节ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/chapters/{chapterId}/video [delete]
func (h *VideoHandler) Delete(c *gin.Context) {
	chapterId := c.Param("chapterId")

	db := database.GetDB()

	// 检查章节是否存在
	var chapter models.Chapter
	if err := db.First(&chapter, chapterId).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Chapter not found",
		})
		return
	}

	// 获取章节视频
	var video models.ChapterVideo
	if err := db.Where("chapter_id = ?", chapter.ID).First(&video).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Video not found",
		})
		return
	}

	// 开始事务：删除版本历史和视频记录
	tx := db.Begin()

	// 删除版本历史
	if err := tx.Where("chapter_video_id = ?", video.ID).Delete(&models.ChapterVideoVersion{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete video versions",
		})
		return
	}

	// 删除视频记录
	if err := tx.Delete(&video).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete video",
		})
		return
	}

	tx.Commit()

	c.JSON(http.StatusOK, gin.H{
		"message": "Video deleted successfully",
	})
}
