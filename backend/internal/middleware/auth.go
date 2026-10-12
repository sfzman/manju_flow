package middleware

import (
	"net/http"
	"strings"
	"time"

	"manju-flow/internal/config"
	"manju-flow/internal/database"
	"manju-flow/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resolveUserByToken 通过 token 解析当前用户
// 优先匹配 sessions 会话表（多会话并存，互不挤下线）；
// 未命中时回退到历史 users.token 列，并把旧 token 懒迁移为会话记录，
// 避免升级部署后已登录用户被强制重新登录。
func resolveUserByToken(db *gorm.DB, token string) (*models.User, bool) {
	// 1. 会话表校验（含过期判断）
	var session models.Session
	if err := db.Where("token = ? AND expires_at > ?", token, time.Now()).First(&session).Error; err == nil {
		var user models.User
		if err := db.First(&user, session.UserID).Error; err == nil {
			return &user, true
		}
		return nil, false
	}

	// 2. 历史单 token 回退 + 懒迁移
	var user models.User
	if err := db.Where("token = ?", token).First(&user).Error; err != nil {
		return nil, false
	}

	migrated := models.Session{
		UserID:    user.ID,
		Token:     token,
		ExpiresAt: time.Now().Add(config.Cfg.Auth.SessionTTL),
	}
	if err := db.Create(&migrated).Error; err == nil {
		// 迁移成功后清空旧列，后续请求直接走会话表；失败不阻塞本次认证
		db.Model(&user).Update("token", "")
	}
	return &user, true
}

// AuthRequired 身份验证中间件
// 从 headers 中获取 Authorization token 验证身份
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取 Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "未提供认证信息",
			})
			c.Abort()
			return
		}

		// 支持 "Bearer <token>" 格式
		token := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "无效的认证信息",
			})
			c.Abort()
			return
		}

		// 查找用户
		user, ok := resolveUserByToken(database.GetDB(), token)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "认证失败，请重新登录",
			})
			c.Abort()
			return
		}

		// 将用户信息存入上下文
		c.Set("user", user)
		c.Set("userId", user.ID)
		c.Set("authToken", token)

		c.Next()
	}
}

// AuthOptional 可选身份验证中间件
// 如果提供了有效的 token，则设置用户信息；否则继续执行
func AuthOptional() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取 Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		// 支持 "Bearer <token>" 格式
		token := authHeader
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}

		if token == "" {
			c.Next()
			return
		}

		// 查找用户
		user, ok := resolveUserByToken(database.GetDB(), token)
		if !ok {
			// token 无效，但不阻止请求
			c.Next()
			return
		}

		// 将用户信息存入上下文
		c.Set("user", user)
		c.Set("userId", user.ID)
		c.Set("authToken", token)

		c.Next()
	}
}
