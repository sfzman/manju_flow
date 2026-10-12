package routes

import (
	"manju-flow/internal/config"
	"manju-flow/internal/handlers"
	"manju-flow/internal/middleware"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// Setup 配置所有路由
func Setup(r *gin.Engine) {
	// 配置 CORS
	// 注意：AllowCredentials 为 true 时，AllowOrigins 不能是 "*"
	// 如果使用 "*"，则关闭 AllowCredentials
	allowAllOrigins := len(config.Cfg.CORS.AllowOrigins) == 1 && config.Cfg.CORS.AllowOrigins[0] == "*"
	r.Use(cors.New(cors.Config{
		AllowOrigins:     config.Cfg.CORS.AllowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: !allowAllOrigins, // "*" 时禁用，指定域名时启用
	}))

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	// API 路由组
	api := r.Group("/api")
	{
		// 认证路由（无需登录）
		authHandler := handlers.NewAuthHandler()
		auth := api.Group("/auth")
		{
			auth.POST("/register", authHandler.Register) // 注册
			auth.POST("/login", authHandler.Login)       // 登录
		}
		// 文件路由（获取文件支持可选认证）
		fileHandler := handlers.NewFileHandler()
		api.GET("/files/*key", middleware.AuthOptional(), fileHandler.Get) // 获取文件（支持公开/私有）

		// 需要认证的路由
		authorized := api.Group("")
		authorized.Use(middleware.AuthRequired())
		{
			// 获取当前用户信息
			authorized.GET("/auth/me", authHandler.GetCurrentUser)
			// 退出登录（删除当前会话，不影响其他已登录设备）
			authorized.POST("/auth/logout", authHandler.Logout)

			// 文件路由（需要认证）
			authorized.POST("/files/original-text", fileHandler.UploadOriginalText) // 上传作品原文 txt
			authorized.POST("/files", fileHandler.Upload)                           // 上传文件
			authorized.DELETE("/files/*key", fileHandler.Delete)                    // 删除文件

			// 书库路由
			bookHandler := handlers.NewBookHandler()
			books := authorized.Group("/books")
			{
				books.GET("", bookHandler.List)                           // 获取书籍列表
				books.POST("", bookHandler.Create)                        // 创建书籍
				books.GET("/:bookId", bookHandler.GetByID)                // 获取书籍详情
				books.PUT("/:bookId", bookHandler.Update)                 // 更新书籍
				books.DELETE("/:bookId", bookHandler.Delete)              // 删除书籍
				books.PUT("/:bookId/outline", bookHandler.UpdateOutline)  // 更新大纲
				books.PUT("/:bookId/art-style", bookHandler.UpdateArtStyle) // 更新画风提示词
				books.PUT("/:bookId/archive", bookHandler.Archive)        // 归档书籍
				books.PUT("/:bookId/unarchive", bookHandler.Unarchive)    // 取消归档
				books.PUT("/:bookId/favorite", bookHandler.Favorite)      // 收藏书籍
				books.DELETE("/:bookId/favorite", bookHandler.Unfavorite) // 取消收藏书籍
			}

			// 角色人设路由
			characterHandler := handlers.NewCharacterHandler()
			characters := authorized.Group("/books/:bookId/characters")
			{
				characters.GET("", characterHandler.List)                                                      // 获取角色列表
				characters.POST("", characterHandler.Create)                                                   // 创建角色
				characters.GET("/:characterId", characterHandler.GetByID)                                      // 获取角色详情
				characters.PUT("/:characterId", characterHandler.Update)                                       // 更新角色
				characters.POST("/:characterId/core-features/generate", characterHandler.GenerateCoreFeatures) // 生成角色核心特征
				characters.DELETE("/:characterId", characterHandler.Delete)                                    // 删除角色
			}

			// 场景资产路由
			sceneAssetHandler := handlers.NewSceneAssetHandler()
			sceneAssets := authorized.Group("/books/:bookId/scene-assets")
			{
				sceneAssets.GET("", sceneAssetHandler.List)                    // 获取场景资产列表
				sceneAssets.POST("", sceneAssetHandler.Create)                 // 创建场景资产
				sceneAssets.GET("/:sceneAssetId", sceneAssetHandler.GetByID)   // 获取场景资产详情
				sceneAssets.PUT("/:sceneAssetId", sceneAssetHandler.Update)    // 更新场景资产
				sceneAssets.DELETE("/:sceneAssetId", sceneAssetHandler.Delete) // 删除场景资产
			}

			// 章节路由
			chapterHandler := handlers.NewChapterHandler()
			chapters := authorized.Group("/books/:bookId/chapters")
			{
				chapters.GET("", chapterHandler.List)           // 获取章节列表
				chapters.POST("", chapterHandler.Create)        // 创建章节
				chapters.POST("/import", chapterHandler.Import) // 从 txt 脚本导入章节
				chapters.GET("/import-tasks", chapterHandler.ListImportTasks)
				chapters.GET("/import-tasks/:taskId", chapterHandler.GetImportTask)
				chapters.GET("/:chapterId", chapterHandler.GetByID)   // 获取章节详情
				chapters.PUT("/:chapterId", chapterHandler.Update)    // 更新章节
				chapters.DELETE("/:chapterId", chapterHandler.Delete) // 删除章节
			}

			// 场景路由
			sceneHandler := handlers.NewSceneHandler()
			scenes := authorized.Group("/books/:bookId/chapters/:chapterId/scenes")
			{
				scenes.GET("", sceneHandler.List)               // 获取场景列表
				scenes.POST("", sceneHandler.Create)            // 创建场景
				scenes.GET("/:sceneId", sceneHandler.GetByID)   // 获取场景详情
				scenes.PUT("/:sceneId", sceneHandler.Update)    // 更新场景
				scenes.DELETE("/:sceneId", sceneHandler.Delete) // 删除场景
			}

			// 帧集路由（分镜绘制，支持多套首尾帧）
			storyboardHandler := handlers.NewStoryboardHandler()
			frameSets := authorized.Group("/scenes/:sceneId/frame-sets")
			{
				frameSets.GET("", storyboardHandler.List)                                                     // 获取帧集列表
				frameSets.POST("", storyboardHandler.Create)                                                  // 创建帧集
				frameSets.GET("/:frameSetId", storyboardHandler.GetByID)                                      // 获取帧集详情
				frameSets.PUT("/:frameSetId", storyboardHandler.Update)                                       // 更新帧集信息
				frameSets.DELETE("/:frameSetId", storyboardHandler.Delete)                                    // 删除帧集
				frameSets.PUT("/:frameSetId/start-frame", storyboardHandler.UpdateStartFrame)                 // 更新起始帧
				frameSets.PUT("/:frameSetId/end-frame", storyboardHandler.UpdateEndFrame)                     // 更新结束帧
				frameSets.GET("/:frameSetId/start-frame/versions", storyboardHandler.ListStartFrameVersions)  // 起始帧版本历史
				frameSets.GET("/:frameSetId/end-frame/versions", storyboardHandler.ListEndFrameVersions)      // 结束帧版本历史
				frameSets.PUT("/:frameSetId/start-frame/revert/:version", storyboardHandler.RevertStartFrame) // 回滚起始帧
				frameSets.PUT("/:frameSetId/end-frame/revert/:version", storyboardHandler.RevertEndFrame)     // 回滚结束帧
			}

			// 动画路由（支持多套动画）
			animationHandler := handlers.NewAnimationHandler()
			authorized.POST("/scenes/:sceneId/animation-prompt/optimize", animationHandler.OptimizePrompt)   // 一键优化动画提示词
			authorized.POST("/scenes/:sceneId/animation-prompt/draft", animationHandler.GeneratePromptDraft) // 一键生成动画提示词草稿
			animations := authorized.Group("/scenes/:sceneId/animations")
			{
				animations.GET("", animationHandler.List)                                                           // 获取动画列表
				animations.POST("", animationHandler.Create)                                                        // 创建动画
				animations.GET("/:animationId", animationHandler.GetByID)                                           // 获取动画详情
				animations.PUT("/:animationId", animationHandler.Update)                                            // 更新动画信息
				animations.DELETE("/:animationId", animationHandler.Delete)                                         // 删除动画
				animations.PUT("/:animationId/upload", animationHandler.Upload)                                     // 上传新版本动画
				animations.GET("/:animationId/generation-tasks", animationHandler.ListGenerationTasks)              // 获取生成任务列表
				animations.POST("/:animationId/generation-tasks", animationHandler.CreateGenerationTask)            // 创建生成任务
				animations.GET("/:animationId/generation-tasks/:taskId", animationHandler.GetGenerationTask)        // 获取生成任务详情
				animations.POST("/:animationId/generation-tasks/:taskId/poll", animationHandler.PollGenerationTask) // 轮询生成任务状态
				animations.GET("/:animationId/versions", animationHandler.ListVersions)                             // 动画版本历史
				animations.PUT("/:animationId/revert/:version", animationHandler.Revert)                            // 回滚动画
			}

			// 音频轨道路由（支持多音频）
			audioHandler := handlers.NewAudioHandler()
			audios := authorized.Group("/scenes/:sceneId/audios")
			{
				audios.GET("", audioHandler.List)                            // 获取音频轨道列表
				audios.POST("", audioHandler.Create)                         // 创建音频轨道
				audios.GET("/:audioId", audioHandler.GetByID)                // 获取单个音频轨道
				audios.PUT("/:audioId", audioHandler.Update)                 // 更新音频轨道信息
				audios.DELETE("/:audioId", audioHandler.Delete)              // 删除音频轨道
				audios.PUT("/:audioId/upload", audioHandler.Upload)          // 上传新版本音频
				audios.POST("/:audioId/generate", audioHandler.Generate)     // AI 生成新版本音频
				audios.GET("/:audioId/versions", audioHandler.ListVersions)  // 音频版本历史
				audios.PUT("/:audioId/revert/:version", audioHandler.Revert) // 回滚音频
			}

			// 章节视频交付路由
			videoHandler := handlers.NewVideoHandler()
			video := authorized.Group("/chapters/:chapterId/video")
			{
				video.GET("", videoHandler.GetInfo)                      // 获取章节视频信息
				video.PUT("", videoHandler.Upload)                       // 上传/更新视频
				video.DELETE("", videoHandler.Delete)                    // 删除视频
				video.PUT("/preview", videoHandler.UploadPreview)        // 上传预览版
				video.PUT("/status", videoHandler.UpdateStatus)          // 更新状态
				video.PUT("/edit-script", videoHandler.UploadEditScript) // 上传/覆盖剪辑脚本 ZIP
				video.GET("/versions", videoHandler.ListVersions)        // 版本历史
				video.PUT("/revert/:version", videoHandler.Revert)       // 回滚版本
			}

			// 场景参考资料路由
			sceneReferenceHandler := handlers.NewSceneReferenceHandler()
			sceneReferences := authorized.Group("/scenes/:sceneId/references")
			{
				sceneReferences.GET("", sceneReferenceHandler.List)                   // 获取参考资料列表
				sceneReferences.POST("", sceneReferenceHandler.Create)                // 创建参考资料
				sceneReferences.POST("/batch", sceneReferenceHandler.BatchCreate)     // 批量创建参考资料
				sceneReferences.GET("/:referenceId", sceneReferenceHandler.GetByID)   // 获取参考资料详情
				sceneReferences.PUT("/:referenceId", sceneReferenceHandler.Update)    // 更新参考资料
				sceneReferences.DELETE("/:referenceId", sceneReferenceHandler.Delete) // 删除参考资料
				sceneReferences.DELETE("", sceneReferenceHandler.DeleteAll)           // 删除全部参考资料
			}

			// LoRA 库路由
			loraHandler := handlers.NewLoraHandler()
			loras := authorized.Group("/loras")
			{
				loras.GET("", loraHandler.List)          // 获取 LoRA 列表
				loras.POST("", loraHandler.Create)       // 创建 LoRA
				loras.GET("/tags", loraHandler.GetTags)  // 获取所有标签
				loras.GET("/:id", loraHandler.GetByID)   // 获取 LoRA 详情
				loras.PUT("/:id", loraHandler.Update)    // 更新 LoRA
				loras.DELETE("/:id", loraHandler.Delete) // 删除 LoRA
			}

			// 评论路由
			commentHandler := handlers.NewCommentHandler()
			// 评论数统计（用于显示徽章）
			authorized.GET("/books/:bookId/scenes/comment-counts", commentHandler.GetSceneCommentCounts)     // 场景评论数
			authorized.GET("/books/:bookId/chapters/comment-counts", commentHandler.GetChapterCommentCounts) // 章节评论数
			// 场景评论（剧本创作、分镜绘制、动画制作、音频后期）
			sceneComments := authorized.Group("/scenes/:sceneId/comments")
			{
				sceneComments.GET("", commentHandler.ListSceneComments)   // 获取场景评论 ?module=script|storyboard|animation|audio
				sceneComments.POST("", commentHandler.CreateSceneComment) // 创建场景评论
			}
			// 章节评论（审核交付）
			chapterComments := authorized.Group("/chapters/:chapterId/comments")
			{
				chapterComments.GET("", commentHandler.ListChapterComments)   // 获取章节评论
				chapterComments.POST("", commentHandler.CreateChapterComment) // 创建章节评论
			}
			// 评论通用操作
			comments := authorized.Group("/comments")
			{
				comments.GET("/:id", commentHandler.GetByID)             // 获取评论详情
				comments.PUT("/:id", commentHandler.Update)              // 更新评论
				comments.DELETE("/:id", commentHandler.Delete)           // 删除评论
				comments.PUT("/:id/resolve", commentHandler.Resolve)     // 标记为已解决
				comments.PUT("/:id/unresolve", commentHandler.Unresolve) // 标记为未解决
			}
		}
	}
}
