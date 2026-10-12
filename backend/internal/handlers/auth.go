package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"time"

	"manju-flow/internal/config"
	"manju-flow/internal/database"
	"manju-flow/internal/models"

	"github.com/gin-gonic/gin"
)

// AuthHandler 认证处理器
type AuthHandler struct{}

// NewAuthHandler 创建认证处理器
func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}

// hashPassword 对密码进行哈希处理
func hashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// generateToken 生成随机 token
func generateToken() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，静默降级会产生全零 token（严重安全问题），直接快速失败
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(bytes)
}

// truncateString 截断字符串到指定字节长度，避免超出列宽导致写入失败
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// createSession 为用户创建新的登录会话，返回会话 token
// 会话之间相互独立，同一账号多端登录不会互相挤下线
func createSession(c *gin.Context, userID uint) (string, error) {
	token := generateToken()
	session := models.Session{
		UserID:    userID,
		Token:     token,
		ExpiresAt: time.Now().Add(config.Cfg.Auth.SessionTTL),
		UserAgent: truncateString(c.Request.UserAgent(), 255),
		IP:        truncateString(c.ClientIP(), 64),
	}
	if err := database.GetDB().Create(&session).Error; err != nil {
		return "", err
	}
	return token, nil
}

// Register 用户注册
// @Summary 用户注册
// @Description 注册新用户（需要在白名单中）
// @Tags auth
// @Accept json
// @Produce json
// @Param request body models.RegisterRequest true "注册信息"
// @Success 201 {object} models.AuthResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Router /api/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	var req models.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	// 检查白名单
	if !config.Cfg.Auth.RegisterWhitelist[req.Username] {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "用户名不在注册白名单中",
		})
		return
	}

	db := database.GetDB()

	// 检查用户名是否已存在
	var existingUser models.User
	if err := db.Where("username = ?", req.Username).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "用户名已存在",
		})
		return
	}

	// 创建用户
	user := models.User{
		Username: req.Username,
		Password: hashPassword(req.Password),
		Nickname: req.Nickname,
	}

	if user.Nickname == "" {
		user.Nickname = user.Username
	}

	if err := db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "注册失败",
		})
		return
	}

	// 注册后直接登录：创建会话
	token, err := createSession(c, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "注册成功但创建会话失败，请重新登录",
		})
		return
	}

	c.JSON(http.StatusCreated, models.AuthResponse{
		Token: token,
		User:  user,
	})
}

// Login 用户登录
// @Summary 用户登录
// @Description 用户登录获取 token
// @Tags auth
// @Accept json
// @Produce json
// @Param request body models.LoginRequest true "登录信息"
// @Success 200 {object} models.AuthResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /api/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
		})
		return
	}

	db := database.GetDB()

	// 查找用户
	var user models.User
	if err := db.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "用户名或密码错误",
		})
		return
	}

	// 验证密码
	if user.Password != hashPassword(req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "用户名或密码错误",
		})
		return
	}

	// 创建新会话（不影响该账号的其他已登录会话）
	token, err := createSession(c, user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "登录失败",
		})
		return
	}

	c.JSON(http.StatusOK, models.AuthResponse{
		Token: token,
		User:  user,
	})
}

// Logout 退出登录
// @Summary 退出登录
// @Description 删除当前会话（仅当前端，不影响该账号的其他已登录设备）
// @Tags auth
// @Produce json
// @Security Bearer
// @Success 200 {object} map[string]string
// @Router /api/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	token := c.GetString("authToken")
	if token != "" {
		if err := database.GetDB().Where("token = ?", token).Delete(&models.Session{}).Error; err != nil {
			// 删除失败不阻塞登出，交由过期清理兜底
			log.Printf("Failed to delete session: %v", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "已退出登录",
	})
}

// GetCurrentUser 获取当前登录用户信息
// @Summary 获取当前用户
// @Description 获取当前登录用户的信息
// @Tags auth
// @Accept json
// @Produce json
// @Security Bearer
// @Success 200 {object} models.User
// @Failure 401 {object} map[string]string
// @Router /api/auth/me [get]
func (h *AuthHandler) GetCurrentUser(c *gin.Context) {
	// 从上下文获取用户（由中间件设置）
	user, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "未登录",
		})
		return
	}

	c.JSON(http.StatusOK, user)
}
