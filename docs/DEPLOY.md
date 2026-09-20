# 阿里云 SAE 部署指南

本文档介绍如何将 Manju Flow 后端部署到阿里云 Serverless 应用引擎 (SAE)。

## 架构概览

```
GitHub (代码) → GitHub Actions (构建) → ACR (镜像仓库) → SAE (运行)
                                                            ↓
Vercel (前端) ←────────────── API ──────────────────── RDS MySQL
```

## 一、阿里云资源准备

### 1. 创建容器镜像服务 ACR

1. 登录 [阿里云控制台](https://cr.console.aliyun.com/)
2. 开通**容器镜像服务 ACR**（个人版免费）
3. 创建**命名空间**，例如：`manju-flow`
4. 创建**镜像仓库**：
   - 仓库名称：`manju-flow-backend`
   - 仓库类型：私有
   - 代码源：本地仓库（我们用 GitHub Actions 推送）

5. 设置**访问凭证**（用于 Docker 登录）：
   - 进入 ACR → 访问凭证
   - 设置固定密码

### 2. 创建 RDS MySQL 数据库

1. 进入 [RDS 控制台](https://rdsnext.console.aliyun.com/)
2. 创建实例：
   - 数据库类型：MySQL 8.0
   - 系列：基础版（开发测试）或 高可用版（生产）
   - 规格：按需选择（1核2G 起步够用）
   - 存储：20GB 起步
   - **地域**：与 SAE 相同（如 cn-hangzhou）

3. 创建数据库：
   - 数据库名：`manju_flow`
   - 字符集：`utf8mb4`

4. 创建账号：
   - 用户名：`manjuflow`
   - 授权数据库：`manju_flow`

5. 设置白名单：
   - 添加 SAE 应用所在的 VPC 网段
   - 或暂时添加 `0.0.0.0/0`（不推荐生产使用）

### 3. 创建 SAE 应用

1. 进入 [SAE 控制台](https://sae.console.aliyun.com/)
2. 创建应用：
   - 应用名称：`manju-flow-backend`
   - 命名空间：选择或创建
   - **VPC**：与 RDS 相同
   - 应用实例数：1（可后续调整）
   - CPU/内存：0.5核1GB 起步

3. 部署配置：
   - 镜像来源：ACR
   - 选择刚才创建的镜像仓库
   - 镜像版本：latest（首次可选）

4. 环境变量配置（重要！）：
   ```
   GIN_MODE=release
   SERVER_PORT=8080
   DB_DRIVER=mysql
   DB_HOST=<RDS内网地址>
   DB_PORT=3306
   DB_USER=manjuflow
   DB_PASSWORD=<数据库密码>
   DB_NAME=manju_flow
   OSS_ENDPOINT=<你的OSS端点>
   OSS_ACCESS_KEY_ID=<AccessKey>
   OSS_ACCESS_KEY_SECRET=<AccessKeySecret>
   OSS_BUCKET_NAME=<Bucket名称>
   CORS_ORIGINS=https://your-app.vercel.app
   # 可选：Wan 3.0 视频生成
   WAN_API_KEY=<阿里云百炼APIKey>
   WAN_WORKSPACE_ID=<百炼业务空间ID>
   WAN_REGION=cn-beijing
   ```

   **CORS_ORIGINS 说明**：
   - 填写你的 Vercel 前端域名，如 `https://manju-flow.vercel.app`
   - 多个域名用逗号分隔：`https://manju-flow.vercel.app,https://custom-domain.com`
   - 开发测试可用 `*` 允许所有来源（不推荐生产使用）

5. 端口配置：
   - 端口：8080
   - 协议：HTTP

6. 健康检查：
   - 路径：`/health`
   - 端口：8080

7. 记录 **SAE 应用 ID**（格式如：`0e3d4e2f-xxxx-xxxx-xxxx-xxxxxxxxxxxx`）

### 4. 创建 RAM 子账号（用于 CI/CD）

1. 进入 [RAM 控制台](https://ram.console.aliyun.com/)
2. 创建用户：
   - 用户名：`github-actions-deployer`
   - 访问方式：编程访问（OpenAPI）

3. 添加权限：
   - `AliyunContainerRegistryFullAccess`（ACR 推送）
   - `AliyunSAEFullAccess`（SAE 部署）

4. 创建 AccessKey 并**妥善保存**

## 二、GitHub Secrets 配置

在 GitHub 仓库 → Settings → Secrets and variables → Actions，添加以下 Secrets：

| Secret 名称 | 说明 | 示例 |
|-------------|------|------|
| `ACR_NAMESPACE` | ACR 命名空间 | `manju-flow` |
| `ACR_USERNAME` | ACR 登录用户名 | 阿里云账号或 RAM 用户名 |
| `ACR_PASSWORD` | ACR 访问凭证密码 | 在 ACR 控制台设置的密码 |
| `ALIYUN_ACCESS_KEY_ID` | RAM 用户 AccessKey ID | `LTAI5t...` |
| `ALIYUN_ACCESS_KEY_SECRET` | RAM 用户 AccessKey Secret | `xxxxxx` |
| `SAE_APP_ID` | SAE 应用 ID | `0e3d4e2f-xxxx-xxxx-xxxx...` |

## 三、部署流程

配置完成后，常规后端部署会自动执行：

1. **推送代码到 main 分支**（修改 backend 目录下的文件）
2. **GitHub Actions 自动触发**：
   - 构建 Docker 镜像
   - 推送到 ACR
   - 通知 SAE 更新部署
3. **SAE 自动拉取新镜像并滚动更新**

### 部署前显式执行场景资产迁移

涉及 `scenes` / `scene_assets` 表结构的版本，**不要依赖服务启动时的 `AutoMigrate`**。部署新后端前：

1. 备份生产数据库。
2. 在维护窗口内使用生产数据库环境变量，在 `backend/` 执行：
   ```bash
   make migrate
   ```
3. 确认命令输出 `Scene asset migration completed successfully` 后，再推送或合并触发部署。

该迁移会修改生产 `scenes` 大表并创建复合外键；未完成迁移前，新后端能启动并注册接口，但场景资产相关请求可能返回 `500`。

### 手动触发部署

在 GitHub → Actions → Deploy Backend to Alibaba Cloud SAE → Run workflow

## 四、前端配置

在 Vercel 中设置环境变量：

```
NEXT_PUBLIC_API_URL=https://<SAE应用公网地址>
```

SAE 公网地址获取：
- SAE 控制台 → 应用详情 → 应用访问设置 → 添加公网 SLB 或启用公网访问

## 五、费用估算（参考）

| 服务 | 规格 | 月费用（约） |
|------|------|-------------|
| SAE | 0.5核1GB × 1实例 | ¥30-50（按量） |
| RDS MySQL | 1核2GB 基础版 | ¥50-80 |
| ACR | 个人版 | 免费 |
| OSS | 按用量 | 约 ¥10 |
| **总计** | - | **¥90-140/月** |

*低流量时 SAE 会自动缩容，实际费用可能更低*

## 六、常见问题

### Q: SAE 应用启动失败？
- 检查环境变量是否正确配置
- 查看 SAE 应用日志
- 确认 RDS 白名单包含 SAE 的 VPC

### Q: 数据库连接失败？
- 确认 RDS 和 SAE 在同一 VPC
- 检查数据库用户名密码
- 确认使用的是 RDS **内网地址**

### Q: GitHub Actions 推送镜像失败？
- 检查 ACR_USERNAME 和 ACR_PASSWORD
- 确认 ACR 命名空间和仓库已创建

### Q: 如何查看应用日志？
- SAE 控制台 → 应用详情 → 日志管理 → 文件日志

## 七、生产环境建议

- [ ] RDS 使用高可用版
- [ ] 开启 RDS 自动备份
- [ ] SAE 配置至少 2 个实例（高可用）
- [ ] 配置 HTTPS（SAE 支持绑定域名 + 证书）
- [ ] 配置告警（CPU/内存/错误率）
- [ ] OSS 开启版本控制
