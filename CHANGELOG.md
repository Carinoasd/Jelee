# Changelog

## Unreleased — Go foundation

- 存取控制統一過濾器（G48.1–G48.4、G48.8、G48.10 子集）：媒體庫授權述詞收斂到 `internal/adapter/postgres/visibility.go` 單一來源，守門測試禁止其他查詢再讀授權表。遷移 000069 新增條目／子樹顯式允許或隱藏（最近者勝）、使用者分級上限（取自 `mpaa`／`certification`，內建美、英、日、德、台分級代碼表）、未分級預設策略、標籤／類型封鎖、管理員是否受限的全域策略；列表、搜尋、詳情、圖片、外掛軌、直投、播放資訊、繼續觀看、統計、相容層全部自動生效。管理員 API `/api/v1/users/{id}/content-access…`、`/api/v1/access/policy`、`/api/v1/access/parental-ratings`，變更寫稽核。仍有規則時拒絕降級。詳見 `docs/access-control.md`。

- Webhook（G12.1–G12.6）：遷移 000068 新增 `webhooks`、`webhook_outbox`、`webhook_deliveries`、`webhook_delivery_attempts`；事件在產生變更的同一交易寫入 outbox，背景投遞器以租約領取、HMAC-SHA256 簽章（`X-Jelee-Signature`／`X-Jelee-Timestamp`）、經 SSRF 防護的出站客戶端送出，至少一次、指數退避加抖動、最大重試與死信、手動重放、投遞日誌可查。端點密鑰與自訂標頭值以 `JELEE_WEBHOOK_MASTER_KEY` 經 AES-GCM 封存；沒有主鑰時不能啟用。管理員 API `/api/v1/webhooks…`、錯誤碼 `webhook_target_denied`（400）。已接上登入成功／失敗／鎖定、掃描完成／失敗、播放開始／停止、NFO 寫回、目錄同步的媒體新增／刪除；其餘事件見 `docs/webhooks.md`。

- 自有 API 條目瀏覽補齊（G34.3）：`GET /api/v1/items` 新增位移形式（`libraryId`、`parentId`、`type`、`sort`＋`order`、`q`、`offset`，回 `total`），游標形式不變；新增一般使用者可讀的 `GET /api/v1/items/{id}/details` 與不含路徑及直投網址的 `GET /api/v1/items/{id}/sources`。網頁條目頁改由伺服器篩選排序，詳情頁改用新 API 並顯示檔案資訊。詳見 `docs/catalog-api.md`。

- 移除工作树中的上游 C# 源码树、.NET 专用 CI、ABI 门禁与开发容器设定；四语 UI 字串移至 `web/src/i18n/<locale>/core.json` 并去除旧产品名，独立版权／归属声明原文移至 `docs/legal/upstream/`。旧源码以 Git 标签 `upstream-csharp-final` 保留，完整品牌扫描零非白名单命中。取代理由与授权待确认事项见 `docs/requirements-clarifications.md`、`docs/LICENSE-COMPLIANCE.md`。

- 第3C3B新增按库默认关闭的NFO只读策略、安全验证摘要及schema6持久快取；正负TTL、行数/字节配额、连续检查点和事务租约核对均有明确上限。库级worker与API仍由3C3C接入，详见 `docs/nfo-cache.md`。

- 第3C3A为只读NFO提供完整内容指纹与独立解析来源；现有单档CLI核对读取前后和当前根/文件身份，读取中改动返回固定 `nfo_changed`。持久快取与按库任务留待后续分段。

- 第3C2B新增显式开启的持久scan/probe请求、增量快取worker、库/条目幂等重建、管理员API/CLI与能力降级；schema5保持前四份迁移不变，详见 `docs/probe-worker.md`。

- 第 2 阶段新增 Argon2id 密码登录、首次管理员初始化、用户生命周期和库 ACL 管理 API；支持限速、失败锁定、会话轮换与撤销、最后管理员保护，以及安全事务审计。
- 新增 schema 2 迁移、账户 API 开关、四语错误与用户语言偏好、严格 JSON 请求校验及账户 OpenAPI schema；详细范围见 `docs/accounts-api.md`。
- 新增独立 Jelee Go 服务、PostgreSQL schema 与迁移命令。
- 新增受会话和库权限限制的目录接口与原文件直投；生产请求拒绝转码相关能力。
- 新增本地用户/令牌创建、单文件注册和基础 doctor 命令。
- 新增 Windows/Linux 项目本地工具链、HTTP/媒体/数据库回归测试与独立 CI。
- 新增只读 NFO 解析、原文字节保留与离线校验 CLI，通过 Windows/Linux 测试。
- 记录上游审计、需求追溯、许可证与回滚边界；保留原源码和历史（原源码后已移出工作树，见最上方条目）。

当前没有发布 SemVer 版本或标签。完整 Web、兼容协议、扫描、图片、运维与全部 G51 工具链仍未完成。NFO 修改后的 XML 序列化、按库批量处理、修复、任务接入及真实客户端往返尚未完成。
