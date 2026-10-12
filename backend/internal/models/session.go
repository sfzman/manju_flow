package models

import "time"

// Session 登录会话
// 一个用户可同时持有多个会话（多浏览器/多设备互不挤下线），
// 登录时创建，登出或过期后删除。
// 取代旧的 users.token 单会话模型（每次登录覆盖旧 token，导致其他端被踢下线）。
type Session struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index;not null" json:"userId"`
	Token     string    `gorm:"size:255;uniqueIndex;not null" json:"-"`
	ExpiresAt time.Time `gorm:"index;not null" json:"-"`
	UserAgent string    `gorm:"size:255" json:"-"`
	IP        string    `gorm:"size:64" json:"-"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"-"`
}

// TableName 指定表名
func (Session) TableName() string {
	return "sessions"
}
