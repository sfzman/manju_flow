package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"manju-flow/internal/config"
	"manju-flow/internal/models"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// Connect 建立数据库连接，不执行任何表结构变更。
func Connect(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	if cfg.Driver != "mysql" {
		return nil, fmt.Errorf("unsupported database driver: %s (only mysql is supported)", cfg.Driver)
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.DBName,
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Info),
		TranslateError:                           true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return db, nil
}

// Init 初始化数据库连接并执行常规启动迁移。
func Init(cfg *config.DatabaseConfig) error {
	db, err := Connect(cfg)
	if err != nil {
		return err
	}

	// 启动即迁移：scene_assets 新表 + scenes 绑定列；MySQL 8 加列基本 instant，
	// 避免再漏跑显式迁移。显式 make migrate 保留作手动兜底。
	if err := db.AutoMigrate(
		&models.User{},
		&models.Session{},
		&models.Book{},
		&models.BookFavorite{},
		&models.Chapter{},
		&models.ChapterImportTask{},
		&models.Scene{},
		&models.SceneAsset{},
		&models.SceneReference{},
		&models.File{},
		&models.SceneFrameSet{},
		&models.SceneFrameSetVersion{},
		&models.SceneAnimation{},
		&models.SceneAnimationVersion{},
		&models.SceneAnimationGenerationTask{},
		&models.SceneAudio{},
		&models.SceneAudioVersion{},
		&models.ChapterVideo{},
		&models.ChapterVideoVersion{},
		&models.Comment{},
		&models.Character{},
		&models.Lora{},
	); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	DB = db
	log.Println("Database connected successfully")
	return nil
}

// GetDB 获取数据库实例
func GetDB() *gorm.DB {
	return DB
}

// CleanExpiredSessions 删除已过期的登录会话
func CleanExpiredSessions() error {
	return DB.Where("expires_at <= ?", time.Now()).Delete(&models.Session{}).Error
}

// StartSessionCleaner 启动后台协程，定期清理过期的登录会话
func StartSessionCleaner(ctx context.Context) {
	go func() {
		// 启动时先清理一次，回收服务停机期间过期的会话
		if err := CleanExpiredSessions(); err != nil {
			log.Printf("Failed to clean expired sessions: %v", err)
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := CleanExpiredSessions(); err != nil {
					log.Printf("Failed to clean expired sessions: %v", err)
				}
			}
		}
	}()
}
