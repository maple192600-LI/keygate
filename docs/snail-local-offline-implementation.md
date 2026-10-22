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


## T03/T07 独立审查整改

审查固定候选 `934c19fde97b083b3fa9a1021f6e72519c1ee20a`，比较 `974b471fb8b6d8160ac646774e10eddcf9751a9a`，只读独立审查确认三个 P2 和一个 P3，已作本包整改：

1. `internal/config/config.go`、`cmd/server/local_offline.go`、`desktop/main.cjs`：首次设置除 Host/Origin 防护外，还要求本次启动器产生的32字节随机能力；由 Electron 主进程仅为本窗口的同源 setup 请求加入私有请求头，不传入页面脚本、不落盘。缺启动身份时本地 production 配置拒绝。已建立 owner 后，原锁内唯一 owner 检查仍防重新初始化。
2. `desktop/runtime.cjs` 与 Go `/health`：启动器发随机32字节挑战，服务用仅启动器与子进程持有的本次能力做带用途域的 HMAC-SHA256 响应。公开 deployment 标记不再作为信任依据；挑战不发送秘密，并在响应后复核子进程存活。未通过身份检查时不开窗口；启动失败仅终止本次生成的未就绪子进程并关闭本次 PG，不信任竞争 HTTP 监听者的关闭接口。
3. `web/src/pages/local-overview.tsx`、`local-offline.tsx`、`local-licenses.tsx`、`internal/handler/offline_lifecycle.go`、`internal/store/offline_lifecycle.go`：有效授权卡进入明确的“有效（含永久）”筛选，只包括已生效未到期和永久证，与概览数量一致。
4. `web/src/pages/local-customers.tsx`：客户停发/恢复后，已有授权列表立即重新读取；停发时档案中的发放按钮禁用，恢复时恢复可用。原服务端拒绝仍保留。

验证：`node --test desktop/runtime.spec.cjs` 2/2，通过伪健康监听者拒绝、挑战合法响应及已退出进程拒绝。任务专属 PostgreSQL 下 `go test ./cmd/server ./internal/config -count=1` 实际通过（非跳过），包含无 Origin/伪 Origin/错能力的 setup 请求都403且 owner仍0，合法能力唯一创建，实际 Go HMAC 响应与独立预期一致。`go build -p 1 -trimpath ... ./cmd/server` 和本地模式 `bun run --cwd web build` 通过。

真实原生管理窗口：`node --test desktop/local-offline.spec.cjs` 1/1（11.7秒），证据 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-nIbggl`。先从窗口之外发同源外观的 setup 请求，实际403；再经正常 UI 首次设置、无邮箱客户、创建隔离签发身份、签发、下载后由 Node v2验证、登记交付、续期、停发恢复、换机及改永久，点击有效卡实际选中 active 并显示永久记录；关闭 PG 确认停止，重开秘密原字节不变，客户档案停发/恢复后表格与按钮同步。新增永久显示断言首次命中期限和截止两个单元格，已改为明确核对两个，不使用 first/强制点击/额外重试。

此记录为源码及原生开发窗口证据，仍非正式包；首次设置对另一 SID 的拒绝以不持启动能力的外部进程请求验证，尚未建立第二真实 Windows 账号做安装验证。T08 在客户端独立候选中验证，管理业务备份/移交和正式双设备安装交付继续待 T09/T10。旧在线到期/订阅写方法的数据库级 offline_file 排除仍待后续 P0 验证，不能仅以本地模式未注册任务代替该项。
