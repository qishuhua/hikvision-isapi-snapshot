# Hikvision ISAPI Snapshot (Go)

海康 IPC / NVR **定时抓图服务**：Go 后端 + 内嵌 Web 控制台 + S3 兼容上传到阿里云 OSS。**单进程即可部署**，没有数据库或前端构建依赖。

> 当前为 MVP（0.1.0），已通过模拟设备的自动测试，但尚未使用你的海康 NVR 固件及阿里云 OSS 账号进行真实设备联调。

## 功能

- **摄像机管理**：添加/编辑/删除设备；配置 URL、用户名、密码、通道 ID；按设备选择 ISAPI `Streaming` / `StreamingProxy` 路径；启用与停用计划。
- **定时抓图**：支持固定间隔（10～86400 秒）、每天多个指定时刻（IANA 时区）、指定日期时间执行一次；重启后保留计划及最后执行槽位。
- **立即抓图**：通过现有摄像机 ID 或传入临时设备参数 API 返回 `image/jpeg`，可选同步上传 OSS 并在响应头返回对象 Key。
- **OSS / S3**：网页配置 HTTPS S3 Endpoint、Region、Bucket、对象前缀、AccessKey ID、AccessKey Secret、STS Token；AWS Signature V4 `PutObject`，不使用 `aws-chunked`。
- **安全与运行**：管理员 Basic Auth、AES-256-GCM 加密敏感配置、并发限制、抓图/上传超时、失败日志及最近 200 条运行记录。
- **单文件网页**：前端嵌入 Go 二进制，不需要 Node.js 或 Nginx 来托管静态文件。

### 系统结构

```text
浏览器管理页 / HTTP API
           │
       Go HTTP 层 ───── JSON 状态文件（AES-GCM 加密密钥）
           │
      Scheduler (1s)
           │
       Worker Pool
           │
      ISAPI HTTP Digest  ──► 海康 IPC / NVR ──► JPEG
           │
       S3 SigV4 PutObject ──► 阿里云 OSS
```

**定时抓图由此 Go 服务主动发起**，不是向设备下发 IPC/NVR 内建定时抓拍配置，也不是历史录像按时间点解码截图。

## 启动

### Docker Compose（推荐）

```bash
cp .env.example .env
# 生成强密码并填入 ADMIN_PASSWORD；密钥填入 DATA_ENCRYPTION_KEY
openssl rand -base64 32
# 将输出填入 .env 内的 DATA_ENCRYPTION_KEY

docker compose up -d --build
curl http://127.0.0.1:8080/api/health
```

浏览器访问 `http://127.0.0.1:8080`，使用 `.env` 的管理员账号密码登录。

Compose 默认**仅监听主机 127.0.0.1:8080**，防止 Basic Auth 密码经公网 HTTP 传输。远程访问请在前面部署 HTTPS 反向代理（或经 SSH 隧道访问），再把 `https://your-domain` 暴露给用户。**不要将 8080 裸露到公网。**

`DATA_ENCRYPTION_KEY` 必须持久保存且与 `state.json` 配套；更换密钥后旧数据将无法解密。Docker 使用 `snapshot-data` 卷持久化状态文件。默认单节点、单实例运行；不要启动多个副本同时修改同一状态文件。

### 直接启动

Go 1.23+：

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD='replace-with-long-random-password'
export DATA_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export HTTP_ADDR='127.0.0.1:8080'
export DATA_PATH='./data/state.json'
go run ./cmd/server
```

注意：上面临时生成的 `DATA_ENCRYPTION_KEY` 只适合首次体验。实际部署必须持久保存，否则程序重启后读取不了已有的加密配置。

## Web 配置步骤

1. **OSS / S3**：填写 S3 Endpoint、Region、Bucket、AK/SK。例：`https://s3.oss-cn-shanghai.aliyuncs.com` + `cn-shanghai`；将 Path-Style 关闭。Bucket 需要预先创建。
2. **摄像机**：`http://172.16.60.5`、用户 `admin`、密码、通道 `201`、抓图模式 `proxy`（NVR 推荐先试该模式）；IPC 可用 `streaming`。
3. **立即抓图验证**：点击“抓图”，确认浏览器能收到 JPEG；点击“抓图+上传”，确认返回 OSS 对象 Key。
4. **定时任务**：选择摄像机，设置每 300 秒、每天 `09:00,12:30,18:00` 或单次具体日期时间。
5. **执行记录**：查看成功/失败、错误、耗时及 OSS Object Key。

阿里云 OSS S3 兼容接口建议使用 `https://s3.oss-{region}.aliyuncs.com` 的 S3 专用 Endpoint 和**虚拟主机样式**（`bucket.s3.oss-...`）；`force_path_style=true` 仅用于兼容其他 S3 服务（例如 SeaweedFS）。阿里云从 2025 年起对某些新建中国内地 Bucket 的默认公网域名访问有额外限制，若上传被拒绝，请核对账号、地域、CNAME/域名及 Bucket 策略。

**对象命名**：`{prefix}/YYYY/MM/DD/{cameraId}/{UTC-timestamp}_{random}.jpg`。建议 Bucket 默认私有，凭证只授予目标 Bucket 的 `PutObject` 权限。

## 立即截图 API

所有 `/api/` 业务接口使用 HTTP Basic Auth，`/api/health` 免认证。下面例子用 HTTP 是为了本机演示；生产请使用 HTTPS。

### A. 指定临时摄像机参数直接抓图

```bash
curl --fail-with-body \
  -u "$ADMIN_USER:$ADMIN_PASSWORD" \
  -H 'Content-Type: application/json' \
  -X POST 'http://127.0.0.1:8080/api/capture' \
  -d '{
    "base_url": "http://172.16.60.5",
    "username": "admin",
    "password": "example-camera-password",
    "channel_id": "201",
    "endpoint_mode": "proxy",
    "upload": false
  }' -o snapshot.jpg
```

成功时 HTTP 200，`Content-Type: image/jpeg`，响应体是 JPEG 字节。

### B. 使用已配置的摄像机直接抓图并同步上传 OSS

```bash
curl --fail-with-body \
  -u "$ADMIN_USER:$ADMIN_PASSWORD" \
  -X POST 'http://127.0.0.1:8080/api/cameras/CAMERA_ID/capture?upload=true' \
  -D response-headers.txt -o snapshot.jpg

# 响应头中包含 X-OSS-Object-Key
```

### C. 管理 API

| 方法 | 路径 | 操作 |
|---|---|---|
| `GET` | `/api/health` | 健康检查（免鉴权） |
| `GET / POST` | `/api/cameras` | 摄像机列表/新增 |
| `GET / PUT / DELETE` | `/api/cameras/{id}` | 摄像机详情/更新/删除 |
| `POST` | `/api/cameras/{id}/capture?upload=true` | 截图并可选上传，响应体 JPEG |
| `POST` | `/api/capture` | 传入摄像机参数并立即截图，响应体 JPEG |
| `GET / POST` | `/api/schedules` | 计划列表/新增 |
| `PUT / DELETE` | `/api/schedules/{id}` | 修改/删除计划 |
| `POST` | `/api/schedules/{id}/run` | 手动运行并上传，响应 JSON |
| `GET / PUT` | `/api/storage` | 查询/修改 OSS 配置（不回显 SK） |
| `GET` | `/api/records?limit=100` | 执行记录 |

**示例任务**：

```json
{"name":"camera-2-every-5-minutes","camera_id":"CAMERA_ID","kind":"interval","interval_seconds":300,"enabled":true}
```

```json
{"name":"day-shift","camera_id":"CAMERA_ID","kind":"daily","times":["09:00","12:30","18:00"],"timezone":"Asia/Shanghai","enabled":true}
```

```json
{"name":"one-off","camera_id":"CAMERA_ID","kind":"once","run_at":"2026-11-20T09:00:00+08:00","enabled":true}
```

## 约束、注意事项

- **设备兼容性**：默认 HTTP Digest 支持 MD5 / SHA-256 的 `qop=auth`，海康不同型号、固件可能对通道映射、鉴权、图片抓取有差异。测试时可在两种路径间切换；例如 NVR 通道 `201` 不等于通道 `2`。
- **任务语义**：间隔任务按 Unix 时间窗对齐，启用后可能立即抓图；每日计划在计划时间后一分钟内触发；单次任务超过目标时间会补一次。调度采用 *claim-then-execute*，已领取的任务遇到服务崩溃不会自动重复；失败记录需要手动重试。
- **精度**：默认 1 秒调度，实际抓图时间受到队列、设备响应和网络延迟影响。不保证毫秒级精确对齐。
- **高负载**：默认最多 4 个并发抓图；队列满时限制排队，可能跳过采样槽位，防止对 NVR 或 OSS 造成过载。
- **MVP 存储**：配置及最多 200 条日志保存在一个原子替换的 JSON 文件中，适合少量到中等数量摄像机的单实例部署；上千路或多副本建议迁移到 SQLite/PostgreSQL + 分布式任务队列。
- **鉴权/部署**：HTTP Basic Auth 只适合配合 HTTPS 的可信管理环境。**临时摄像机地址允许管理端指定内网目标**，不要向不可信调用方开放 API，否则可被滥用为 SSRF。建议使用 API 网关、请求审计及可访问主机白名单。
- **凭证管理**：设备密码、OSS SK/Token 使用 AES-256-GCM 持久化；服务器管理员权限仍可访问解密后的凭证。STS Token 存储为静态值，临时凭证过期后需要重新填写。建议使用有最小权限的 RAM AccessKey 或后续实现 STS 自动刷新。
- **S3 兼容范围**：自行实现了 PutObject 所需的 AWS SigV4 子集（未引入第三方 AWS SDK），已通过本地签名测试，但**尚未完成阿里云 OSS 实机上传验证**；网络、DNS、CNAME、区域、签名差异需在真实环境中联调。
- **图片类型**：仅接受完整 JPEG；摄像机返回 XML 错误、BMP、Multipart MJPEG 时会报错。最大图片 16 MiB。

## 测试

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/server
```

自动测试覆盖 ISAPI Digest 握手、IPC/NVR 抓图路径、S3 SigV4 请求签名、配置加密持久化、定时调度去重及 HTTP API。

## 参考

- [海康 ISAPI 开发指南中的实时抓图接口](https://raw.githubusercontent.com/loozhengyuan/hikvision-sdk/master/resources/isapi.pdf)
- [阿里云：使用 AWS SDK 访问 OSS（S3 兼容接口）](https://www.alibabacloud.com/help/zh/oss/developer-reference/use-aws-sdks-to-access-oss)
- [阿里云：OSS 与 S3 API 兼容性及差异](https://www.alibabacloud.com/help/en/oss/developer-reference/compatibility-with-amazon-s3)
