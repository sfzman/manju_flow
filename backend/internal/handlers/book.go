package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"manju-flow/internal/database"
	"manju-flow/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BookHandler 书籍处理器
type BookHandler struct{}

// NewBookHandler 创建书籍处理器
func NewBookHandler() *BookHandler {
	return &BookHandler{}
}

// List 获取书籍列表
// @Summary 获取书籍列表
// @Description 获取作品库中的所有作品，支持分页、状态和关键词过滤
// @Tags books
// @Accept json
// @Produce json
// @Param page query int false "页码" default(1)
// @Param size query int false "每页数量" default(10)
// @Param keyword query string false "搜索关键词（标题或作者）"
// @Param favorite query bool false "仅返回当前用户收藏的作品"
// @Success 200 {object} models.BookListResponse
// @Router /api/books [get]
func (h *BookHandler) List(c *gin.Context) {
	db := database.GetDB()

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 10
	}
	offset := (page - 1) * size

	// 过滤参数
	keyword := c.Query("keyword")
	statusParam := c.Query("status")
	favoriteOnly, _ := strconv.ParseBool(c.Query("favorite"))
	userID := c.GetUint("userId")

	// 构建查询
	query := db.Model(&models.Book{})
	if favoriteOnly {
		query = query.Where("id IN (?)", db.Model(&models.BookFavorite{}).
			Select("book_id").Where("user_id = ?", userID))
	}

	// 状态过滤（支持逗号分隔多值，如 ?status=NONE,IN_PROGRESS）
	if statusParam != "" {
		statuses := []string{}
		for _, s := range strings.Split(statusParam, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			statuses = append(statuses, s)
		}
		if len(statuses) > 0 {
			query = query.Where("adaptation_status IN ?", statuses)
		}
	}

	// 关键词搜索（标题或作者）
	if keyword != "" {
		query = query.Where("title LIKE ? OR author LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to count books",
		})
		return
	}

	// 获取数据
	var books []models.Book
	if err := query.Order("created_at DESC").Offset(offset).Limit(size).Find(&books).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to fetch books",
		})
		return
	}
	if err := markFavoriteBooks(db, userID, books); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch favorites"})
		return
	}

	c.JSON(http.StatusOK, models.BookListResponse{
		Total: total,
		Page:  page,
		Size:  size,
		Data:  books,
	})
}

// Create 创建书籍
// @Summary 创建新书籍
// @Description 向作品库中添加新作品
// @Tags books
// @Accept json
// @Produce json
// @Param book body models.CreateBookRequest true "书籍信息"
// @Success 201 {object} models.Book
// @Failure 400 {object} map[string]string
// @Router /api/books [post]
func (h *BookHandler) Create(c *gin.Context) {
	var req models.CreateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	book := models.Book{
		Title:               req.Title,
		Author:              req.Author,
		Cover:               req.Cover,
		CoverLandscape:      req.CoverLandscape,
		Description:         req.Description,
		Outline:             req.Outline,
		ArtStyle:            req.ArtStyle,
		OriginalTextKey:     req.OriginalTextKey,
		OriginalTextPreview: req.OriginalTextPreview,
		AdaptationStatus:    models.AdaptationStatusInProgress,
	}

	db := database.GetDB()
	if err := db.Create(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to create book",
		})
		return
	}

	c.JSON(http.StatusCreated, book)
}

// GetByID 根据ID获取书籍详情
// @Summary 获取书籍详情
// @Description 根据ID获取书籍的详细信息
// @Tags books
// @Accept json
// @Produce json
// @Param id path int true "书籍ID"
// @Success 200 {object} models.Book
// @Failure 404 {object} map[string]string
// @Router /api/books/{id} [get]
func (h *BookHandler) GetByID(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}
	books := []models.Book{book}
	if err := markFavoriteBooks(db, c.GetUint("userId"), books); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch favorite status"})
		return
	}
	book = books[0]

	c.JSON(http.StatusOK, book)
}

// Update 更新书籍信息
// @Summary 更新书籍
// @Description 更新书籍的信息
// @Tags books
// @Accept json
// @Produce json
// @Param id path int true "书籍ID"
// @Param book body models.CreateBookRequest true "书籍信息"
// @Success 200 {object} models.Book
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/books/{id} [put]
func (h *BookHandler) Update(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	var req models.CreateBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	book.Title = req.Title
	book.Author = req.Author
	book.Cover = req.Cover
	book.CoverLandscape = req.CoverLandscape
	book.Description = req.Description
	book.OriginalTextKey = req.OriginalTextKey
	book.OriginalTextPreview = req.OriginalTextPreview

	if err := db.Save(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update book",
		})
		return
	}

	c.JSON(http.StatusOK, book)
}

// Delete 删除书籍
// @Summary 删除书籍
// @Description 从书库中删除书籍（软删除）
// @Tags books
// @Accept json
// @Produce json
// @Param id path int true "书籍ID"
// @Success 200 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/books/{id} [delete]
func (h *BookHandler) Delete(c *gin.Context) {
	id := c.Param("bookId")

	db := database.GetDB()
	result := db.Delete(&models.Book{}, id)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to delete book",
		})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Book deleted successfully",
	})
}

// UpdateOutline 更新书籍大纲
// @Summary 更新书籍大纲
// @Description 单独更新书籍的故事大纲
// @Tags books
// @Accept json
// @Produce json
// @Param id path int true "书籍ID"
// @Param outline body models.UpdateOutlineRequest true "大纲内容"
// @Success 200 {object} models.Book
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/books/{id}/outline [put]
func (h *BookHandler) UpdateOutline(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	var req models.UpdateOutlineRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	book.Outline = req.Outline

	if err := db.Save(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update outline",
		})
		return
	}

	c.JSON(http.StatusOK, book)
}

// UpdateArtStyle 更新书籍画风提示词
// @Summary 更新书籍画风提示词
// @Description 单独更新书籍的画风提示词，供动画制作模块生成视频时追加到提示词末尾
// @Tags books
// @Accept json
// @Produce json
// @Param id path int true "书籍ID"
// @Param artStyle body models.UpdateArtStyleRequest true "画风提示词内容"
// @Success 200 {object} models.Book
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /api/books/{id}/art-style [put]
func (h *BookHandler) UpdateArtStyle(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	var req models.UpdateArtStyleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	book.ArtStyle = req.ArtStyle

	if err := db.Save(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to update art style",
		})
		return
	}

	c.JSON(http.StatusOK, book)
}

// Archive 归档书籍
// @Summary 归档书籍
// @Description 将书籍标记为已归档，从默认列表中隐藏
// @Tags books
// @Produce json
// @Param id path int true "书籍ID"
// @Success 200 {object} models.Book
// @Failure 404 {object} map[string]string
// @Router /api/books/{id}/archive [put]
func (h *BookHandler) Archive(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	book.AdaptationStatus = models.AdaptationStatusArchived
	if err := db.Save(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to archive book",
		})
		return
	}

	c.JSON(http.StatusOK, book)
}

// Unarchive 取消归档
// @Summary 取消归档书籍
// @Description 将书籍从归档状态恢复为创作中
// @Tags books
// @Produce json
// @Param id path int true "书籍ID"
// @Success 200 {object} models.Book
// @Failure 404 {object} map[string]string
// @Router /api/books/{id}/unarchive [put]
func (h *BookHandler) Unarchive(c *gin.Context) {
	id := c.Param("bookId")

	var book models.Book
	db := database.GetDB()
	if err := db.First(&book, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Book not found",
		})
		return
	}

	book.AdaptationStatus = models.AdaptationStatusInProgress
	if err := db.Save(&book).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to unarchive book",
		})
		return
	}

	c.JSON(http.StatusOK, book)
}

// Favorite 收藏作品。
func (h *BookHandler) Favorite(c *gin.Context) {
	db := database.GetDB()
	bookID := c.Param("bookId")

	var book models.Book
	if err := db.Select("id").First(&book, bookID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Book not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch book"})
		return
	}

	favorite := models.BookFavorite{UserID: c.GetUint("userId"), BookID: book.ID}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&favorite).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to favorite book"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"isFavorite": true})
}

// Unfavorite 取消收藏作品。
func (h *BookHandler) Unfavorite(c *gin.Context) {
	db := database.GetDB()
	bookID := c.Param("bookId")

	var book models.Book
	if err := db.Select("id").First(&book, bookID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Book not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch book"})
		return
	}

	if err := db.Where("user_id = ? AND book_id = ?", c.GetUint("userId"), book.ID).
		Delete(&models.BookFavorite{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unfavorite book"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"isFavorite": false})
}

func markFavoriteBooks(db *gorm.DB, userID uint, books []models.Book) error {
	if len(books) == 0 {
		return nil
	}

	bookIDs := make([]uint, 0, len(books))
	for _, book := range books {
		bookIDs = append(bookIDs, book.ID)
	}

	var favoriteBookIDs []uint
	if err := db.Model(&models.BookFavorite{}).
		Where("user_id = ? AND book_id IN ?", userID, bookIDs).
		Pluck("book_id", &favoriteBookIDs).Error; err != nil {
		return err
	}

	favorites := make(map[uint]struct{}, len(favoriteBookIDs))
	for _, bookID := range favoriteBookIDs {
		favorites[bookID] = struct{}{}
	}
	for i := range books {
		_, books[i].IsFavorite = favorites[books[i].ID]
	}
	return nil
}
