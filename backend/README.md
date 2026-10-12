# ManjuFlow 后端服务

漫剧制作流程管理系统后端 API 服务。

## 技术栈

- Go 1.24
- Gin (Web 框架)
- GORM (ORM)
- SQLite / MySQL (数据库)

## 快速开始

### 本地开发

```bash
# 安装依赖
make tidy

# 运行服务（默认使用 SQLite）
make run
```

服务启动后访问 http://localhost:8080

### Docker 部署

```bash
# 在项目根目录运行
docker-compose up -d
```

### 场景资产显式迁移

场景资产绑定会修改生产环境中的大型 `scenes` 表（新增列、索引和复合外键）。不要依赖服务启动时的 `AutoMigrate` 完成该变更。

部署包含本功能的版本前：

1. 备份生产数据库。
2. 在维护窗口内进入 `backend/` 执行 `make migrate`。
3. 确认迁移成功后再发布后端服务。

迁移会：

- 为 `scene_assets` 创建 `(book_id, code)` 唯一索引。
- 将场景资产改为硬删除。
- 为 `scenes` 回填 `book_id`，新增 `scene_asset_code` 和内部列 `scene_asset_book_id`。
- 将旧的 `scene_asset_id` 绑定转换为 `scene_asset_code`。
- 创建 `(scene_asset_book_id, scene_asset_code)` 复合外键；资产删除时自动清空绑定，编号更新时自动级联。`scene_asset_book_id` 仅用于外键，不会出现在 API 中，避免删除资产时影响 `scenes.book_id`。

## 认证与会话

- 登录/注册成功后创建独立会话（`sessions` 表），返回随机 token。
- **一个账号可同时保持多个会话**：多浏览器/多设备登录互不挤下线；每次登录新增一条会话，不再覆盖旧 token。
- 会话有效期由 `SESSION_TTL` 控制（默认 30 天），过期会话由后台协程每小时清理一次。
- `POST /api/auth/logout` 删除当前会话（仅当前端，不影响同账号其他设备）。
- 兼容升级：旧版 `users.token` 中的存量 token 在首次请求时自动懒迁移为会话记录，已登录用户无需重新登录。

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| SERVER_PORT | 服务端口 | 8080 |
| GIN_MODE | Gin 模式 (debug/release) | debug |
| DB_DRIVER | 数据库驱动 (sqlite/mysql) | sqlite |
| DB_HOST | MySQL 主机 | localhost |
| DB_PORT | MySQL 端口 | 3306 |
| DB_USER | MySQL 用户名 | root |
| DB_PASSWORD | MySQL 密码 | - |
| DB_NAME | 数据库名 | manju_flow |
| CORS_ORIGINS | 允许的跨域来源 | * |
| SESSION_TTL | 登录会话有效期，Go duration 格式（如 720h）或纯小时数 | 720h |
| OSS_ENDPOINT | 阿里云 OSS 端点 | - |
| OSS_ACCESS_KEY_ID | OSS AccessKey ID | - |
| OSS_ACCESS_KEY_SECRET | OSS AccessKey Secret | - |
| OSS_BUCKET_NAME | OSS Bucket 名称 | - |
| TTS_API_URL | 豆包音频生成服务地址 | https://openspeech.bytedance.com/api/v3/tts/create |
| TTS_API_KEY | 豆包音频生成服务 API Key | - |
| TTS_MODEL | 音频生成模型 | seed-audio-1.0 |
| TTS_AUDIO_FORMAT | 生成音频格式（wav/mp3/pcm/ogg_opus） | mp3 |
| ARK_API_BASE_URL | 火山引擎 Ark 服务地址 | https://ark.cn-beijing.volces.com |
| ARK_API_KEY | 火山引擎 Ark API Key | - |
| WAN_API_BASE_URL | 阿里云百炼万相服务地址；留空时由 Workspace ID 和地域自动拼接 | - |
| WAN_API_KEY | 阿里云百炼万相 API Key | - |
| WAN_WORKSPACE_ID | 阿里云百炼业务空间 ID | - |
| WAN_REGION | 万相服务地域 | cn-beijing |
| ARK_AGENT_PLAN_API_BASE_URL | 火山引擎 Ark Agent Plan 服务地址，用于 LLM / 图片理解 | https://ark.cn-beijing.volces.com/api/plan |
| ARK_AGENT_PLAN_API_KEY | 火山引擎 Ark Agent Plan API Key | - |
| ARK_AGENT_PLAN_SUPPORTED_LLM_MODELS | Ark Agent Plan 支持的 LLM 模型 ID 列表，逗号分隔或 JSON 数组；第一个为默认模型 | doubao-seed-2-1-pro-260628 |

## API 接口

### 作品库管理

#### 获取作品列表

```
GET /api/books
```

查询参数：
- `page` - 页码（默认 1）
- `size` - 每页数量（默认 10）
- `keyword` - 搜索关键词（标题/作者）

响应示例：
```json
{
  "total": 100,
  "page": 1,
  "size": 10,
  "data": [
    {
      "id": 1,
      "title": "仙剑遗志",
      "author": "林枫",
      "cover": "https://example.com/cover.jpg",
      "description": "...",
      "adaptationStatus": "IN_PROGRESS",
      "adaptedBy": "陈艾利克斯",
      "chapterCount": 120,
      "createdAt": "2024-01-01T00:00:00Z",
      "updatedAt": "2024-01-01T00:00:00Z"
    }
  ]
}
```

#### 创建作品

```
POST /api/books
Content-Type: application/json

{
  "title": "仙剑遗志",
  "author": "林枫",
  "cover": "https://example.com/cover.jpg",
  "description": "一段仙侠传奇..."
}
```

#### 获取作品详情

```
GET /api/books/:id
```

#### 更新作品

```
PUT /api/books/:id
Content-Type: application/json

{
  "title": "仙剑遗志（修订版）",
  "author": "林枫",
  "cover": "https://example.com/cover2.jpg",
  "description": "修订后的描述..."
}
```

#### 更新画风提示词

```
PUT /api/books/:id/art-style
Content-Type: application/json

{
  "artStyle": "日系赛璐璐动画风格，高饱和色彩，柔和光影..."
}
```

说明：画风提示词与作品一对一、可选。动画制作模块创建生成任务时若开启 `appendArtStyle` 且画风提示词非空，会自动将其追加到提交给视频模型的提示词末尾；`appendArtStyle` 缺省时默认开启。

#### 删除作品

```
DELETE /api/books/:id
```

### 健康检查

```
GET /health
```

## 项目结构

```
backend/
├── cmd/
│   └── main.go              # 程序入口
├── internal/
│   ├── config/              # 配置管理
│   ├── database/            # 数据库连接
│   ├── handlers/            # API 处理器
│   ├── models/              # 数据模型
│   └── routes/              # 路由配置
├── Dockerfile
├── Makefile
└── go.mod
```
