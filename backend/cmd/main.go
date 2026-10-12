package main

import (
	"context"
	"log"

	"manju-flow/internal/config"
	"manju-flow/internal/database"
	"manju-flow/internal/handlers"
	"manju-flow/internal/oss"
	"manju-flow/internal/routes"
	"manju-flow/utils"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

func init() {
	// 加载.env文件(仅在本地开发环境)
	if utils.GetEnv("APP_ENV", "local") == "local" {
		// 尝试从项目根目录加载
		err := godotenv.Load(".env")
		if err != nil {
			// 尝试从当前目录加载
			err = godotenv.Load(".env")
			if err != nil {
				log.Printf("无法加载 .env 文件: %v", err)
			}
		}
	}
}

func main() {
	// 加载配置
	cfg := config.Load()

	// 设置 Gin 模式
	gin.SetMode(cfg.Server.Mode)

	// 初始化数据库
	if err := database.Init(&cfg.Database); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 启动登录会话过期清理
	database.StartSessionCleaner(context.Background())

	// 初始化 OSS（可选，如果未配置则跳过）
	if cfg.OSS.Endpoint != "" {
		if err := oss.Init(&cfg.OSS); err != nil {
			log.Printf("Warning: Failed to initialize OSS: %v", err)
			log.Println("File upload/download features will be disabled")
		} else {
			log.Println("OSS initialized successfully")
		}
	} else {
		log.Println("OSS not configured, file upload/download features will be disabled")
	}

	// 创建 Gin 引擎
	r := gin.Default()

	// 启动后台动画生成任务轮询器，避免页面关闭后任务长时间无人轮询而过期
	handlers.NewAnimationHandler().StartGenerationTaskPoller(context.Background())
	// 启动章节导入任务工作器，服务重启后会从数据库继续未完成任务
	handlers.NewChapterHandler().StartImportTaskWorker(context.Background())

	// 配置路由
	routes.Setup(r)

	// 启动服务器
	addr := ":" + cfg.Server.Port
	log.Printf("Server starting on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
