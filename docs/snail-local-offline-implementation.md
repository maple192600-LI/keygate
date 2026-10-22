# 蜗牛客户与授权中心实施检查点

状态：源码开发中，未发布；当前运行入口仍为开发 Electron 主程序，不是正式双击交付包。

## 固定范围

官方基线 `ed141b6b29bd4c5e6a45f5cb36e757458da7af72`，保留上游 LICENSE、NOTICE、Powered by Keygate 链接与 API 归属。中文、本地离线，Keygate 为唯一客户与授权台账。按用户 2026-10-23 最新决定使用原生 Go、捆绑 PostgreSQL、Electron 独立窗口，不使用 Docker；不注册 OTP、在线激活、支付和客户门户，不启动在线生命周期、邮件或遥测任务。原 Node/SQLite 管理程序不进入本程序。

唯一业务规格是用户完整设计包中的《01_蜗牛授权管理_开发实施方案.md》及 contracts，原型仅提供页面和动作位置。来源 ZIP SHA-256 `229c7ee712c9e08f0cf3b067fe17c4bf4acc3bfbf665f712d75911b032a9f1e8`。

## 修改与证据

| 工作包 | 实际路径 | 已验证行为 | 尚未完成 |
| --- | --- | --- | --- |
| T03 | `desktop/`、`cmd/server/local_offline.go`、`internal/auth/`、`internal/{handler,store}/local_auth.go`、本地认证迁移 | 先生成运行秘密，再 initdb；隔离原生 PostgreSQL 启停；无邮箱 owner、原子唯一初始化、登录/改密/会话撤销、Host/Origin、路由隔离 | 正式管理包、一键入口的安装验证；原生在线数据方法逐项防护 |
| T04 | `customers.go`、`offline_requests.go`、商业表迁移、`web/src/pages/local-customers.tsx` | 无邮箱客户、同名独立 UUID、来源回读、严格申请及完整设备码、重复申请与冲突 | 最终整体页面验收 |
| T05 | `internal/offline/key_store.go`、`internal/service/offline_issuance.go`、对应 handler/store、`local-signing.tsx`、`local-issue.tsx` | Argon2id/AES-GCM 加密身份、Ed25519 原字节签发、事务原件、同操作号并发、锁定及签名/归档/审计后失败无半成品、超过 24 小时原操作重放、原件下载 | 最终独立审查及真实正式身份交接 |
| T07 | `offline_lifecycle.go`、`offline_deliveries.go`、`local-licenses.tsx`、`local-overview.tsx`、`local-state-form.tsx` | 续期/永久/换机/停发恢复、最新版本交付独立状态、原件历史、当前及历史设备、使用显示未回传、客户/授权查询与概览 | 最终 P0 场景、复制说明和身份移交完整验收 |

定向命令在任务专属 Windows/PostgreSQL 环境实际执行；DSN 只从仓库外运行秘密读取，不能写入本文或日志：

- `go test ./cmd/server ./internal/offline ./internal/auth ./internal/config -count=1`：通过。显式提供任务 DSN 及本次 Node v2 路径，固定向量与跨语言检查实际运行；Electron 桥接专用用例需额外隔离目录，普通执行的该跳过不计闭环证据。
- `go test ./internal/service -run '^TestOffline' -count=1 -v`：事务故障四场景、25 小时幂等、两窗口续期冲突、换机原期限与历史通过。首版独立测试的时刻未按服务预览的整秒基准固定，修正测试时钟；换机测试和预备 UI 曾误带新期限，服务正确拒绝，现已移除该错误字段。
- `bun run --cwd web build`（`VITE_SNAIL_DEPLOYMENT=local_offline`）：类型检查与生产前端构建通过。未将此构建当安装包。
- `node --test desktop/local-offline.spec.cjs`：实际独立窗口、原生 PostgreSQL、合成无邮箱客户、创建签发身份、签发下载、Node 独立验签、下载不记交付、手动登记、续期、停发/恢复、换机、永久及关闭重开通过。最新证据 `C:/Users/Administrator/AppData/Local/Temp/snail-keygate-t03-FxIxWp`，含状态、原件和实际截图，供固定候选审查后清理。
- 客户端跨仓闭环 `playwright test --config playwright.licensing.config.ts`：客户端真实导出申请，经本服务实际 HTTP/事务签发，真实 Electron 导入并初始化管理员、坏文件保留及重开通过；见客户端已有任务记录。该场景预选原生文件对话框返回路径，不是正式安装包或跨设备证据。

## 失败与修正

Windows `pg_ctl start` 捕获标准流会因后台数据库继承管道而挂起，已使用原生日志参数和独立标准流后实际重测。默认并发 Go 编译曾出现编译器内部崩溃，`GOMAXPROCS=2 go build -p 1 -trimpath` 成功，未宣称确定编译器根因。

上游前端静态路由测试在 Windows 首次执行失败，原因是对 URL 使用 `filepath.Clean` 后以斜杠前缀判断；已改为 URL 路径清理，并让本地模式复用该静态服务，原用例通过。没有引入在线路由。

实际 UI 核对发现过浮层未继承局部主题和交付登记后摘要不更新，已修正并在原生窗口复测。未靠增加重试或跳过断言掩盖失败。

## 后续依赖

客户端 T08 仍须逐项审查 history 写方法及到期保护；T09 完整加密备份/恢复、空实例业务移交及历史操作者快照尚未实现；T10 两个正式 Windows 包、用户控制的正式身份、原生安装、真实不同 MachineGuid 验证尚未完成；最后固定候选执行完整检查、独立审查和第 13 节 P0。现阶段不能用于真实商业客户交付。
