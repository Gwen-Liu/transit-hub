# Fork 定制与上游更新维护

本页是 Fork 定制的维护导航，具体行为以对应源码和测试为准。各版本的变化见[发布说明](releases/)。

## 当前定制与回归入口

下列定制已在历史提交中保存；更新作者代码时逐项回查直接实现和测试，而不是用上游整文件覆盖。表中后端模块简称以 `backend/internal/modules/` 为前缀。

| 维护责任 | 主要实现 owner | 直接回归入口 |
| --- | --- | --- |
| 北京时间今日成本、同步日过滤与 Key 明细日期 | `upstream/platform_service.go`；`dashboard/metrics_service.go` | `upstream/platform_service_key_usage_today_test.go`；`dashboard/metrics_service_purchase_test.go` |
| 管理员会话有效性与 my-sites 会话同步 | `my_sites/service.go`；`dashboard/service.go` | `my_sites/service_status_test.go`；`dashboard/service_refresh_test.go` |
| 旧库 priority_mode 升级兼容 | `backend/internal/database/migrations/000018_connection_health_strategy_mode.sql` | 同目录 `runner_test.go` |
| 公网目标、DNS 重验、重定向及站点协议 | `backend/internal/security/egress/policy.go`；upstream 与 connection_health 默认客户端 | `egress/http_policy_test.go`（位于 security 下）、`upstream/http_compat_test.go`、`connection_health/http_compat_test.go`、`backend/internal/httpserver/upstream_http_test.go` |
| 认证、验证码、登录限速与凭据最小展示 | `auth/`、`my_sites/connection_service.go`；`GET /api/users` 已删除，不以加登录校验的方式恢复 | `auth/security_test.go`、`my_sites/credential_preview_test.go` |
| Ticket/Lottery 嵌入身份绑定与撤销 | `tickets/`、`lottery/service.go` | 同模块身份、来源与会话撤销回归 |
| 请求/响应预算与工作区调度隔离 | `backend/internal/shared/httpjson/`、`backend/internal/httpserver/server.go`、`settings/`、`upstream/service.go` | 请求/响应上限、`settings/strategy_workspace_test.go`、`upstream/refresh_scheduler_test.go` |
| 真实对接幂等预留与补偿边界 | `my_sites/connection_service.go`、`repository.go`；迁移 `000019` | `my_sites/connection_idempotency_test.go`、`backend/internal/database/migrations/idempotency_migration_test.go` |
| New API 对接协议跟随所选分组、Sub2API 上游分组平台（含 Grok）自动跟随与错误边界 | `my_sites/connection_protocol.go`、`connection_service.go`、`handler.go`；前端 `GroupRatesView.vue` 与 `utils/connectionOptions.ts` | `my_sites/connection_protocol_test.go`；`frontend/tests/connections.test.ts` |
| 自然周/月、日结重试、后台成本采样、成本记录标志与跨日缓存 | `frontend/src/modules/admin/utils/dashboardMetrics.ts`；`dashboard/metrics_service.go`、`metrics_repository.go`；`upstream/daily_usage.go`、`session_refresh.go`、`stale_sync.go` | `frontend/tests/dashboardMetrics.test.ts`；dashboard日结/真实PG测试；`upstream/daily_usage_test.go`、`sync_resilience_test.go` |
| Fork 部署来源与发布版本一致性 | `backend/internal/config/config.go`、`scripts/verify-release.mjs`、`.github/workflows/release.yml` | `node scripts/verify-release.mjs` 与发布工作流 |

## HTTP 站点兼容调整

上游站点支持公网 HTTP 和 HTTPS。这是对 Fork 早期仅允许 HTTPS 的调整，公网访问边界保持不变。

- 站点业务使用统一的公网 HTTP/HTTPS 策略：URL 规范化、登录、管理员与上游指标读取、已保存站点同步、模型发现和探活共用该策略；不填写协议时仍默认 HTTPS。公网 IP 与合法非默认端口不因格式被拒绝。
- 公网目标校验仍在入口、实际请求、每跳重定向及拨号前执行；私网、环回、链路本地、特殊用途地址和混合 DNS 不因此开放。显式 HTTP 入口不授权 HTTPS 重定向降级；跨来源敏感 Header 与带请求体的重放由统一策略控制。
- 通知和匿名嵌入等非站点业务保留原 HTTPS-only 客户端；不因上游支持 HTTP 而跟随放宽。测试必须同时证明新站点入口可用与原 HTTPS-only 拒绝行为仍有效。
- 早先创建失败且没有 Session 的站点，需在编辑界面重新提交该站点认证信息并保存，成功建立会话后再同步；单纯刷新不会重新构造缺失凭据。凭据不写入维护记录或测试证据。

## 与作者 1.x 的关系

作者从 v1.0.0 起把产品改成只读的经营分析后台，删除了分组对接、调价映射与自动调价、活动调价、分组健康、抽奖、排行榜和旧版嵌入工单。Fork 继续维护 0.1.x 并保留这些功能，不合并作者 1.x；作者的修复逐个判断，对 Fork 也成立的单独移植。成本口径两边相同（上游当天实际消费 × 该上游充值倍率），作者 `e3ca89b` 的“站点定价”只作用于营收，Fork 日结不替换为作者的定稿逻辑。

已移植（对照作者 `upstream/main` = `e9e14d9`，后端路径同样以 `backend/internal/modules/` 为前缀）：

| 作者提交 | Fork 中的实现 | 回归入口 |
| --- | --- | --- |
| `9c8731f` 上游 Key 列表逐页拉取 | `upstream/platform_service.go` | `upstream/platform_service_key_list_test.go` |
| `82bc5b7` 探活回退使用账号列表里的 base URL | `upstream/platform_accounts.go`、`probe_credentials.go` | `upstream/platform_accounts_test.go`、`probe_credentials_test.go` |
| `b680019` 删除 `GET /api/users` | `backend/internal/httpserver/server.go`；`users` 模块已删除 | 无（接口与模块已不存在） |
| `e3ca89b` 中的 New API 本站营收与分组用量改用全站 `/api/log/stat` | `upstream/daily_usage.go`、`platform_service.go`；上游消费仍用 `/api/log/self/stat` | `upstream/daily_usage_test.go`、`platform_service_group_daily_stats_test.go` |
| `1f07c9d` 兼容迁移遵守 search_path，Fork 的 `000019` 同样处理 | 迁移 `000016`、`000018`、`000019` | `backend/internal/database/migrations/runner_postgres_test.go`（需 `TEST_DATABASE_URL`） |
| `e9a91e1` 新版 new-api 鉴权（短期 JWT、刷新 Cookie 轮换、个人访问令牌、用户 ID 可选、二次验证提示）与进程内刷新合并 | `upstream/newapi_auth.go`、`session_refresher.go`、`platform_service.go`；`dashboard/service.go`；前端 `AdminLoginModal.vue`、`UpstreamView.vue` | `upstream/newapi_auth_test.go`、`session_refresh_newapi_test.go`；`dashboard/service_newapi_auth_test.go` |
| `9d631fd` 凭据静态加密（AES-256-GCM，密文绑定存储位置） | `backend/internal/shared/secretbox/`、`shared/datadir/`；`upstream/repository.go`、`site_cache.go`；`my_sites/repository.go`；`dashboard/repository.go`；`settings/repository.go`；`backend/internal/httpserver/server.go` | `backend/internal/credentialstest/`（需 `TEST_DATABASE_URL`，Redis 部分需 `TEST_REDIS_URL`）；`upstream/site_cache_secret_test.go`、`my_sites/repository_secret_test.go` |

移植时的 Fork 差异：
- 会话写回沿用 Fork 的 `upstream/session_refresh.go`（同站点加锁、刷新后立即写回），不采用作者的 `writeBackRefreshedSession`；遇到 401 的强制续期扩展到 new-api，并经过进程内刷新合并，不重放已轮换的 refresh token。
- 凭据加密额外覆盖作者没有的上游站点 Redis 缓存（`sealedSession` 字段）和对接记录 `real_connections.upstream_key`；自有站会话解不开时只清空会话、保留分组映射（自动调价依赖它）。作者的出站代理加密与版本弹窗提示不移植（Fork 没有这两个功能），改为启动日志提示。
- `shared/datadir` 的路径清理改用 `path.Clean`，让测试在 Windows 上也能运行；Linux 上行为不变。数据目录配置为 `DATA_DIR`，默认 `data`，镜像中为 `/app/data`。

Fork 自己补的同类修复：`dashboard/service.go` 的 `RefreshAdminSession` 刷新出新令牌时先保存再校验，避免校验失败时丢失已轮换的令牌（对应作者 `e9a91e1` 的会话轮换部分），回归入口 `dashboard/service_refresh_test.go`。

待移植：暂无。不移植：在线更新（下载地址写死作者仓库）、TransitLink 客户端配置与工单、出站代理。

## 更新作者版本时的最小流程

1. 核对当前工作树和现有分支，先辨明每个未提交文件的来源。个人 Fork 为 `origin`，作者为只读 `upstream`；只获取已有目标引用，记录当前 Fork、作者和共同祖先，不为更新擅自创建分支。
2. 做三方比较：共同祖先→Fork 定制、共同祖先→作者新增、当前工作树→HEAD。仅图表/文档更新与业务更新分开判断；对上表每个受影响 owner 检查作者是否已吸收、实现是否等价、测试是否仍覆盖。旧草稿不直接覆盖正式实现。
3. 在获准的现有分支完成最小整合；旧定制只有在需求明确被替代或已被作者等价吸收且验证成立时退出。冲突或尚未采用的旧草稿差异保留为待处理证据，不静默选边。
4. 执行后端 `go test -count=1 -timeout=3m ./...`、`go test -race -count=1 -timeout=3m ./...`、`go vet ./...`、`go build ./...`；前端 `npm run test:dashboard`、`npm run test:connections`、`npm run typecheck`、`npm run build`；根目录 `node scripts/verify-release.mjs` 与差异空白检查。真实 PostgreSQL 使用独立 `TEST_DATABASE_URL`，跳过不算集成通过；环境缺口不借生产依赖补测。
5. 独立检查同一最终来源和全部直接消费者后，按闭包显式选择文件形成可审查提交，记录测试及未验证边界。`.local/`、`_skill-cache/`、本机状态、凭据、临时二进制与恢复包不进入公开提交；不要使用整树盲目提交。
6. 只把对所有部署都有用的产品改动发成版本，只为修正某个站点自己数据的代码不进产品，范围见[什么能发成版本](releases/README.md#什么能发成版本)。发布继续以 `defaultAppVersion` 和现有发布一致性脚本/工作流为准，不套用其他项目的版本规则；Release 正文（`docs/releases/vX.Y.Z.md`）按[发布说明怎么写](releases/README.md)的固定结构和模板写，提交信息与 Release 都不加 AI 署名。提交、推送、发布、生产切换是不同动作；缺少对应授权时停在当前已完成层。撤销本次修复使用本次提交的反向补丁，不硬重置带有既有修改的工作树，不恢复旧数据库来撤回纯代码变更。
