# Hikvision ISAPI Snapshot (Go)

海康 IPC / NVR **定时抓图服务**：Go 后端 + 内嵌 Web 控制台 + S3 兼容上传到阿里云 OSS。**单进程即可部署**，没有数据库或前端构建依赖。

> 当前为 MVP（0.1.0），已通过模拟设备的自动测试，但尚未使用你的海康 NVR 固件及阿里云 OSS 账号进行真实设备联调。

## 功能

- **摄像机管理**：添加/编辑/删除设备；配置 URL、用户名、密码、通道 ID；按设备选择 ISAPI `Streaming` / `StreamingProxy` 路径；启用与停用计划。
- **NVR 多通道采集**：在摄像机页面填写 `all` 或逗号分隔的流通道 ID，选择 Streaming 路径；每次任务先查询通道名称和在线状态，仅抓取在线通道。可设置分辨率和抓图、通道查询重试次数。
- **定时抓图**：支持固定间隔（10～86400 秒）及每天多个指定时刻（IANA 时区），一个计划可选择多个设备；重启后保留计划及最后执行槽位。
- **立即抓图**：通过现有摄像机 ID 或传入临时设备参数 API 返回 `image/jpeg`，可选同步上传 OSS 并在响应头返回对象 Key。
- **OSS / S3**：网页配置 HTTPS S3 Endpoint、Region、Bucket、对象前缀、AccessKey ID、AccessKey Secret、STS Token；AWS Signature V4 `PutObject`，不使用 `aws-chunked`。
- **安全与运行**：AES-256-GCM 加密敏感配置、并发限制、抓图/上传超时、失败日志及最近 200 条运行记录。
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
# 生成密钥并填入 DATA_ENCRYPTION_KEY
openssl rand -base64 32
# 将输出填入 .env 内的 DATA_ENCRYPTION_KEY

docker compose up -d --build
curl http://127.0.0.1:8080/hk_isapi/health
```

浏览器访问 `http://127.0.0.1:8080/hk_isapi/index.html`，无需服务内登录。

服务本身不提供访问鉴权，Compose 默认仅监听主机 `127.0.0.1:8080`。远程访问时请在 Nginx 中配置访问控制及 HTTPS，并保护网页和所有接口。

`DATA_ENCRYPTION_KEY` 必须持久保存且与 `state.json` 配套；更换密钥后旧数据将无法解密。Docker 使用 `snapshot-data` 卷持久化状态文件。默认单节点、单实例运行；不要启动多个副本同时修改同一状态文件。

### 直接启动

Go 1.23+：

```bash
export DATA_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export HTTP_ADDR='127.0.0.1:8080'
export DATA_PATH='./data/state.json'
go run ./cmd/server
```

注意：上面临时生成的 `DATA_ENCRYPTION_KEY` 只适合首次体验。实际部署必须持久保存，否则程序重启后读取不了已有的加密配置。

## Web 配置步骤

1. **OSS / S3**：填写 S3 Endpoint、Region、Bucket 和访问密钥；Bucket 需要预先创建。
2. **添加设备**：选择 NVR 或直连摄像机，填写地址、用户名和密码。查询并保存时读取设备名称；NVR 同时读取通道名称、状态和流通道编号。通道名称是 NVR 配置的名称，不保证是摄像机自身名称。
3. **立即抓图验证**：保存后点击“抓图”或“抓图+上传”，验证配置的默认通道。
4. **配置计划**：选择设备；NVR 可勾选具体通道，或选择整个 NVR（每次执行重新发现在线通道，包含后续新增通道），可选择多台设备，再设置间隔或每日时刻。新建设备默认请求 1920×1080 截图。
5. **执行记录**：查看成功、失败、错误、耗时及对象 Key。

阿里云 OSS S3 兼容接口建议使用 `https://s3.oss-{region}.aliyuncs.com` 的 S3 专用 Endpoint 和**虚拟主机样式**（`bucket.s3.oss-...`）；`force_path_style=true` 仅用于兼容其他 S3 服务（例如 SeaweedFS）。阿里云从 2025 年起对某些新建中国内地 Bucket 的默认公网域名访问有额外限制，若上传被拒绝，请核对账号、地域、CNAME/域名及 Bucket 策略。

**对象命名**：`{prefix}/YYYY/MM/DD/{cameraId}/{UTC-timestamp}_{random}.jpg`。建议 Bucket 默认私有，凭证只授予目标 Bucket 的 `PutObject` 权限。

## 立即截图 API

服务的网页及所有接口均不设置内部鉴权；需要访问控制时由前置 Nginx 提供。下面例子使用本机 HTTP。

### A. 指定临时摄像机参数直接抓图

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -X POST 'http://127.0.0.1:8080/hk_isapi/capture' \
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
  -X POST 'http://127.0.0.1:8080/hk_isapi/cameras/CAMERA_ID/capture?upload=true' \
  -D response-headers.txt -o snapshot.jpg

# 响应头中包含 X-OSS-Object-Key
```

旧版 `/api/*`、`/api/v1/isapi/*` 和 `/isapi/*` 路径仍可使用，作为兼容入口；新调用建议使用 `/hk_isapi/*`。各路径行为一致。

### C. 管理 API

| 方法 | 路径 | 操作 |
|---|---|---|
| `GET` | `/hk_isapi/health` | 健康检查 |
| `POST` | `/hk_isapi/discover` | 用设备连接参数发现设备和 NVR 通道 |
| `GET` | `/hk_isapi/cameras/{id}/discover` | 重新发现已保存设备和通道 |
| `GET / POST` | `/hk_isapi/cameras` | 摄像机列表/新增 |
| `GET / PUT / DELETE` | `/hk_isapi/cameras/{id}` | 摄像机详情/更新/删除 |
| `POST` | `/hk_isapi/cameras/{id}/capture?upload=true` | 截图并可选上传，响应体 JPEG |
| `POST` | `/hk_isapi/capture` | 传入摄像机参数并立即截图，响应体 JPEG |
| `GET / POST` | `/hk_isapi/schedules` | 计划列表/新增 |
| `PUT / DELETE` | `/hk_isapi/schedules/{id}` | 修改/删除计划 |
| `POST` | `/hk_isapi/schedules/{id}/run` | 手动运行并上传，响应 JSON |
| `GET / PUT` | `/hk_isapi/storage` | 查询/修改 OSS 配置（不回显 SK） |
| `GET` | `/hk_isapi/records?limit=100` | 执行记录 |

**示例任务**（`targets` 可包含多个设备，NVR 可指定 `target_channels` 为 `all` 或通道列表）：

```json
{"name":"camera-2-every-5-minutes","camera_id":"CAMERA_ID","kind":"interval","interval_seconds":300,"enabled":true}
```

```json
{"name":"day-shift","targets":[{"camera_id":"NVR_ID","target_channels":"all"},{"camera_id":"IPC_ID"}],"kind":"daily","times":["09:00","12:30","18:00"],"timezone":"Asia/Shanghai","enabled":true}
```



## 约束、注意事项

- **设备兼容性**：默认 HTTP Digest 支持 MD5 / SHA-256 的 `qop=auth`，海康不同型号、固件可能对通道映射、鉴权、图片抓取有差异。测试时可在两种路径间切换；例如 NVR 通道 `201` 不等于通道 `2`。
- **批量采集限制**：同一设备的查询和截图串行，不同设备可并发；上传仍由抓图工作线程同步执行，并非独立上传队列。对象路径仍沿用本项目原格式。间隔计划仍按时间槽执行，同一计划运行中不会重复排队；未实现采集器的完成后等待语义。
- **任务语义**：间隔任务按 Unix 时间窗对齐，启用后可能立即抓图；每日计划在计划时间后一分钟内触发；旧单次计划不再执行，可在网页删除。调度采用 *claim-then-execute*，已领取的任务遇到服务崩溃不会自动重复；失败记录需要手动重试。
- **精度**：默认 1 秒调度，实际抓图时间受到队列、设备响应和网络延迟影响。不保证毫秒级精确对齐。
- **高负载**：默认最多 4 个并发抓图；队列满时限制排队，可能跳过采样槽位，防止对 NVR 或 OSS 造成过载。
- **MVP 存储**：配置及最多 200 条日志保存在一个原子替换的 JSON 文件中，适合少量到中等数量摄像机的单实例部署；上千路或多副本建议迁移到 SQLite/PostgreSQL + 分布式任务队列。
- **鉴权/部署**：服务本身不提供鉴权，应由 Nginx 或其他网关负责访问控制。**临时摄像机地址允许管理端指定内网目标**，不要向不可信调用方开放 API，否则可被滥用为 SSRF。建议使用 API 网关、请求审计及可访问主机白名单。
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
