# 蜗牛客户与授权中心实施检查点

当前状态：管理端0.1.1已完成原生Windows双击交付包、中文一键入口和原程序位置更新，账号及资料保留，无需Docker。固定管理产品9844b41实际包已验证五主页面、四设置页签、签发下载/手动交付及正常启停重开。正式身份由用户控制，客户包双设备、续期、换机、重装及r11到期保护实测通过。客户a096dd7包另补齐干净Windows的Microsoft运行库，实际安装授权链、首次管理员门禁、错误证书保留及正常重开卸载通过，补证独立复审No findings。完整源码原运行仍为874通过/8失败、退出1；对应场景隔离定向7/8通过，剩余故障注入测试修正后两变体2/2通过，不冒充整层全绿，也未扩展会计或税务功能。最终P0、Git整合及收尾继续，尚未发布。

## 固定范围

官方基线 `ed141b6b29bd4c5e6a45f5cb36e757458da7af72`，保留上游 LICENSE、NOTICE、Powered by Keygate 链接与 API 归属。中文、本地离线，Keygate 为唯一客户与授权台账。按用户 2026-10-23 最新决定使用原生 Go、捆绑 PostgreSQL、Electron 独立窗口，不使用 Docker；不注册 OTP、在线激活、支付和客户门户，不启动在线生命周期、邮件或遥测任务。原 Node/SQLite 管理程序不进入本程序。

唯一业务规格是用户完整设计包中的《01_蜗牛授权管理_开发实施方案.md》及 contracts，原型仅提供页面和动作位置。来源 ZIP SHA-256 `229c7ee712c9e08f0cf3b067fe17c4bf4acc3bfbf665f712d75911b032a9f1e8`。

## 修改与证据

| 工作包 | 实际路径 | 已验证行为 | 尚未完成 |
| --- | --- | --- | --- |
| T03 | `desktop/`、`cmd/server/local_offline.go`、`internal/auth/`、`internal/{handler,store}/local_auth.go`、本地认证迁移 | 先生成运行秘密，再 initdb；隔离原生 PostgreSQL 启停；无邮箱 owner、原子唯一初始化、登录/改密/会话撤销、Host/Origin、路由隔离；0.1.1实际Windows包和一键入口启停重开通过 | 最终整体交付复核与收尾；数据层防护见后文 A19 |
| T04 | `customers.go`、`offline_requests.go`、商业表迁移、`web/src/pages/local-customers.tsx` | 无邮箱客户、同名独立 UUID、来源回读、严格申请及完整设备码、重复申请与冲突；固定包五主页面与四设置页签实测 | 最终P0证据归并 |
| T05 | `internal/offline/key_store.go`、`internal/service/offline_issuance.go`、对应 handler/store、`local-signing.tsx`、`local-issue.tsx` | Argon2id/AES-GCM 加密身份、Ed25519 原字节签发、事务原件、同操作号并发、锁定及签名/归档/审计后失败无半成品、超过 24 小时原操作重放、原件下载；用户正式身份实际签发三版并独立验签 | 最终独立审查；未解密用户正式私钥，通用移交恢复由隔离工程身份验证 |
| T07 | `offline_lifecycle.go`、`offline_deliveries.go`、`local-licenses.tsx`、`local-overview.tsx`、`local-state-form.tsx` | 续期/永久/换机/停发恢复、最新版本交付独立状态、原件历史、当前及历史设备、使用显示未回传、客户/授权查询与概览；正式客户安装器双设备续期换机、停发后旧证仍按原期限有效，r11运行到期保护通过 | 最终P0结论、完整客户端结果与收尾仍待完成 |

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

## 早期检查点的后续依赖（进展见当前状态及末节）

客户端 T08 已有独立源码候选和真实到期流程证据，见客户端任务记录；T09 源码和原生窗口已实现并验证，仍待本包独立审查及原客户续期导入的跨仓补证；T10 两个正式 Windows 包、用户控制的正式身份、原生安装、真实不同 MachineGuid 验证尚未完成；最后固定候选执行完整检查、独立审查和第 13 节 P0。现阶段不能用于真实商业客户交付。


## T03/T07 独立审查整改

审查固定候选 `934c19fde97b083b3fa9a1021f6e72519c1ee20a`，比较 `974b471fb8b6d8160ac646774e10eddcf9751a9a`，只读独立审查确认三个 P2 和一个 P3，已作本包整改：

1. `internal/config/config.go`、`cmd/server/local_offline.go`、`desktop/main.cjs`：首次设置除 Host/Origin 防护外，还要求本次启动器产生的32字节随机能力；由 Electron 主进程仅为本窗口的同源 setup 请求加入私有请求头，不传入页面脚本、不落盘。缺启动身份时本地 production 配置拒绝。已建立 owner 后，原锁内唯一 owner 检查仍防重新初始化。
2. `desktop/runtime.cjs` 与 Go `/health`：启动器发随机32字节挑战，服务用仅启动器与子进程持有的本次能力做带用途域的 HMAC-SHA256 响应。公开 deployment 标记不再作为信任依据；挑战不发送秘密，并在响应后复核子进程存活。未通过身份检查时不开窗口；启动失败仅终止本次生成的未就绪子进程并关闭本次 PG，不信任竞争 HTTP 监听者的关闭接口。
3. `web/src/pages/local-overview.tsx`、`local-offline.tsx`、`local-licenses.tsx`、`internal/handler/offline_lifecycle.go`、`internal/store/offline_lifecycle.go`：有效授权卡进入明确的“有效（含永久）”筛选，只包括已生效未到期和永久证，与概览数量一致。
4. `web/src/pages/local-customers.tsx`：客户停发/恢复后，已有授权列表立即重新读取；停发时档案中的发放按钮禁用，恢复时恢复可用。原服务端拒绝仍保留。

验证：`node --test desktop/runtime.spec.cjs` 2/2，通过伪健康监听者拒绝、挑战合法响应及已退出进程拒绝。任务专属 PostgreSQL 下 `go test ./cmd/server ./internal/config -count=1` 实际通过（非跳过），包含无 Origin/伪 Origin/错能力的 setup 请求都403且 owner仍0，合法能力唯一创建，实际 Go HMAC 响应与独立预期一致。`go build -p 1 -trimpath ... ./cmd/server` 和本地模式 `bun run --cwd web build` 通过。

真实原生管理窗口：`node --test desktop/local-offline.spec.cjs` 1/1（11.7秒），证据 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-nIbggl`。先从窗口之外发同源外观的 setup 请求，实际403；再经正常 UI 首次设置、无邮箱客户、创建隔离签发身份、签发、下载后由 Node v2验证、登记交付、续期、停发恢复、换机及改永久，点击有效卡实际选中 active 并显示永久记录；关闭 PG 确认停止，重开秘密原字节不变，客户档案停发/恢复后表格与按钮同步。新增永久显示断言首次命中期限和截止两个单元格，已改为明确核对两个，不使用 first/强制点击/额外重试。

此记录为源码及原生开发窗口证据，仍非正式包；首次设置对另一 SID 的拒绝以不持启动能力的外部进程请求验证，尚未建立第二真实 Windows 账号做安装验证。T08 在客户端独立候选中验证，管理业务备份/移交和正式双设备安装交付继续待 T09/T10。旧在线到期/订阅写方法的数据库级 offline_file 排除仍待后续 P0 验证，不能仅以本地模式未注册任务代替该项。


## T09 备份与移交、A19 数据层收口

实际修改：`internal/store/offline_bundle.go`、`internal/store/offline_snapshot.go`、`internal/store/offline_write.go`、`internal/service/offline_backup.go`、`internal/handler/offline_backup.go`、`internal/offline/backup.go`、`internal/handler/local_write_gate.go`、`cmd/server/local_restore.go`、本地路由、`desktop/runtime.cjs`、`web/src/pages/local-data-settings.tsx` 和本地 API。迁移 `20261024_snail_actor_history` 将历史操作者快照与登录用户分开；`20261025_snail_offline_write_guard` 为离线授权设置事务写用途并阻止原生在线写入、用户绑定、激活、订阅、用量、席位、浮动会话、附加服务、计费、通知与原生维护期续费。签发、停发、bundle 和恢复分别只在对应事务使用明确用途，不由页面提供。保留在线记录的原行为。

完整备份是版本化的数据快照：在管理写锁内对全部 public 数据表和序列做 PostgreSQL repeatable-read 读取，连同配套运行加密秘密、公开身份和经明确确认的可选加密身份，使用独立备份密码以固定 Argon2id/AES-256-GCM 参数整体加密。归档不含可执行 SQL；恢复只用安装程序的可信迁移建新库，严格核对迁移校验和、字段、关系、原件签名和摘要，并逐表回读。外键仅在新库事务中延后到提交前完整检查，随后恢复原约束属性，不禁用触发器。插入计划会产生的临时时间历史仅在新候选中按快照ID剔除，源库及签发/交付原件从不改写。迁移元数据的应用时间采用目标新库的实际建库时间；业务表、原件和历史时间保持快照值。

确认恢复后，先撤销候选中原会话、增加 owner 的 auth_version，再原子发布受保护运行配置并切换同一监听器的处理器；新请求使用新库和新 JWT 秘密。旧库保留，未发布成功时继续使用原库和原会话。重开由原生启动器读取已选数据库与身份目录。未包含身份的备份仍有公开身份，后续只允许导入同一身份。

业务包以固定表词汇导出完整产品/计划定义、权益、商业客户、申请、授权、设备历史、签发原件（raw_bytes 的 PostgreSQL 十六进制表示）、SHA-256、交付、管理操作和操作者快照。禁止登录凭据和私钥，内部 KG 码在新库重新生成并以新库秘密加密。产品和计划按合同代码/固定 slug 及定义映射；其余商业 UUID 和 revision 原值保留。预览回滚不写商业数据，确认时重新验证空实例条件，在一个事务中导入，失败整体回滚。

实际验证：

- 任务专属 PostgreSQL 下，`go test ./cmd/server ./internal/service ./internal/offline ./internal/handler ./internal/auth ./internal/config -run '^(TestLocalOffline|TestOffline|TestBackupEncryption|TestLocalWriteGate|TestPassword|TestLocalPassword)' -count=1` 通过。数据库场景未跳过；专用 Electron 跨仓桥接另行执行，不作为普通 Go 命令的替代。
- 业务移交测试：不同 owner、不同运行加密秘密的空实例；保留客户/授权/申请/设备/原件/交付/历史操作者ID；原件同字节同 SHA；客户及授权停发的 false 保留；恢复续发后同一身份签出下一版、独立核对原截止加7天。hash、最高版本、外键、计划定义和未知字段错误均拒绝且没有半成品；非空目标拒绝。
- 完整备份测试：随机加密不同字节、用途/参数/未知字段/重复字段拒绝；错误密码、密文损坏、配套配置、错误关联及无效管理密码哈希拒绝；包含/不包含身份两种备份均验证；失败和取消确认源数据及候选清理结果。
- 恢复 HTTP 测试：备份后新增第二客户并改管理密码，预览仍为原1客户；故意改变运行配置使发布失败，原2客户及当前会话保留；恢复后实际返回原1客户、原密码，旧 JWT/刷新令牌拒绝，原数据库仍有2客户，运行配置持久保存新目标。
- 迁移测试：从先前 T03/T07 的合成数据快照建旧版 schema，再升级操作者与离线写防护迁移；原文件字节、摘要及 created_by 不变，历史名称正确补齐。不是旧客户迁移器。
- A19：原生到期、停用、撤销、改期限、恢复、用户关联、激活、订阅及 Stripe 邮箱写入直接调用不能改变离线整行元数据，原件仍一致。原在线 SetLicenseValidUntil/Reinstate/Expire 定向正向测试通过；增加对所有原生在线子记录的保护后，在另建隔离库完整运行 `go test ./internal/store -count=1` 通过（14.494秒），并确认库清理成功。
- `node --test desktop/runtime.spec.cjs` 既有挑战身份场景；本包原生窗口实际测试 `node --test desktop/local-offline.spec.cjs` 通过。完整源窗口：创建客户、4版签发、交付、状态、商家保存、导出2版快照、勾选及确认带身份备份、错误密码保留输入、恢复至2版、旧会话重新登录、关闭重开、另一空实例导入、单独身份导入/解锁、续出第3版并由 Node v2独立验签。最新证据根及结果见固定候选收尾段；移交后的最近操作同时显示来源操作者快照和新实例操作者，未将历史操作者导入为登录账号。

真实窗口发现商家 GET 读取结果可能覆盖已输入内容，已在读取完成前禁用编辑和保存，再实测持久保存与重开。测试曾误把恢复后新建立的合法会话当成必须再次登录，以及未等待目标 setup 结束就给旧表单填登录密码；已按实际会话合同和可见登录状态修正测试，没有增加重试或强制点击。原失败根保留供审查。

Jev 节点真实调用：`jev-1.14.0` 对当前 T09 设计和证据判断 aligned 0.76、drift 0.15、insufficient 0.09，confidence 0.64；“可宣称全部完成”为0.07。输入719、输出59 tokens。该结果只指导核查，不替代实际测试和独立审查；未宣称持续后台监控。

限制及下一依赖：当前导入格式按当前迁移版本严格验证，最大128 MiB；不是跨任意版本的通用迁移平台。原客户客户端导入移交后续期文件还需补跨仓实测；正式管理包、客户安装包、用户掌握的正式身份、不同 MachineGuid Windows 安装及最终P0仍待 T10/T11。中文操作说明已写入 `docs/蜗牛客户与授权中心_本地操作说明.md`，包路径与哈希在正式交付时固定。

固定候选收尾证据（T09源码，不是正式包）：最后一次相关 Go 验证包含完整 store 包、原生路由/签发、移交、完整备份、操作者迁移和恢复切换；最后的清理归属整改再次运行完整备份与恢复切换用例通过（5.726秒/3.177秒）。恢复操作以同一预览ID只记一条审计；运行配置发布失败后按原预览重试，不重复记“恢复”。该场景最后再次通过（2.927秒）。随后重建 Go 程序并实际执行 `node --test desktop/runtime.spec.cjs desktop/local-offline.spec.cjs`：3/3通过、无跳过，真实窗口场景22.687秒，证据根 `C:/Users/Administrator/AppData/Local/Temp/snail-keygate-t03-523fhg`。保留该根、先前失败根及两个工程工作树，用于本包独立审查、跨仓补证和正式包构建；尚未到最终收尾清理阶段。

本候选实际运行的 Go 程序 SHA-256：`9af2ba1855aa0d1a3e753bd969e9fe737f24b6f97096a3a736d64e02e28e2d0e`。该二进制位于任务工作树的忽略目录 `bin/`，未提交或发布。


## T09 独立审查整改（2026-10-23）

固定候选 75be626b543aaebd79f408d24b382b51da07a0d5 对 fc0e3ab6b29c4e95ba8e46236c7ded19903b44fc 的独立只读审查提出三项证实问题：P2 恢复发布遗漏数据库连接配置变化；P3 备份与业务移交导出可能超过自身读取上限。

实际整改：desktop/runtime.cjs 从同一次读取的 runtime.json 原字节计算 SHA-256，经本次私有 Go 子进程环境传入；cmd/server/local_restore.go 在发布前核对完整摘要，发布后保存新摘要供再次恢复使用。摘要不进入网页、业务包或完整备份。配置冲突拒绝切换并保留当前资料与会话。internal/offline/backup.go 为 AES-GCM、Base64 与 JSON 留出边界并核对实际编码后长度；internal/store/offline_bundle.go 在发布最终业务包前核对同一 128MiB 上限，超限明确失败。

实际验证：TestLocalOfflineRestoreSwitchAndRuntimeFailurePreservesSource 通过（3.639秒），分别修改格式合法的 jwtSecret、databasePassword、databasePort、identityDirectory，均拒绝；源库两名客户和会话保留，同预览恢复原配置后成功且恢复审计只有一条。TestBackup* 通过（1.990秒），含真实约96MiB边界字节的加密/解密回读，原会导出超限的长度明确拒绝。TestBusinessBundleExportRefusesUnreadableSize 通过（0.471秒）：真实序列化恰为128MiB可输出、多一字节拒绝。该序列化边界测试不冒充大数据库导入的性能验收。

Go重新构建后，node --test desktop/runtime.spec.cjs desktop/local-offline.spec.cjs 实际3/3通过（25.9秒），证据 C:/Users/Administrator/AppData/Local/Temp/snail-keygate-t03-GbZfIv；当前合成管理窗口备份恢复、关闭重开、移交续期全部执行。Go执行文件SHA256为9c2c1d35adbe68b6535bd32f76ebcb484c3178827a833d6d3a6ab631b66e30e2。源码已改，待整改定向独立复审；该记录不构成正式安装包验收。

审查期间曾怀疑对不应有的在线关联记录删除/移走违反A19；复核原句后证据不足，审查撤回该项。未应用的额外保留迁移尝试已撤回，未修改既有迁移或数据。不为不存在的旧客户开发修复或迁移路径。


### T09 复审与原客户端移交续期

14f251982292cc61896c315abe56b5e1410809f0 相对75be626的三项整改已完成独立只读复审，No findings；审查不运行测试，不冒充安装包或大数据库性能验收。

cmd/server/client_bridge_test.go 扩展到两个独立初始化的合成管理库，经真实 HTTP 导出业务包、空实例预览/导入、逐字节核对原件、单独导入加密签发身份、续期。客户端原 policy 和安装不变，desktop-client/tests/electron/keygate-client-acceptance.spec.ts 在同一原 Electron 上导入新管理端续期文件，确认同licenseId/keyId、revision=2、原截止再加7天，正常重开仍是2。实际 playwright test --config playwright.licensing.config.ts 一项通过（14.5秒），证据 C:/Users/Administrator/AppData/Local/Temp/snail-v2-closure-PLfvqW。签发私钥只在本轮隔离管理根中，测试只向客户端传公开策略、申请和签名原件；不是正式客户安装包。

### T10 独立管理运行包准备

新增 desktop/package.json 与 npm锁文件、ensure-runtime.cjs、package.cjs、run.bat、stop.bat，以及中文构建/对应源码说明。用户明确要求不用Docker，故以原生Electron窗口管理本地Go/PostgreSQL。pack入口固定已提交干净源码后编译Go和中文网页，包含PostgreSQL bin/lib/share与其原始许可，附对应源码ZIP及LICENSE/NOTICE；运行包不带数据库、管理私钥或TEST_ONLY文件。

新增正常停止命令：第二实例仅请求原窗口正常退出，不启动新的数据库、不强杀。main.cjs 也等待正在启动的服务，退出途中不另开窗口。原生窗口流程扩展实际调用停止入口，PG状态确认已停止；本次3/3通过（22.4秒），证据 C:/Users/Administrator/AppData/Local/Temp/snail-keygate-t03-wgVRm3。第一稿仅测试变量 stopped 与本文件既有变量重名，Node语法检查失败；已改为 managerStopped 后实际全流程通过，未改产品行为迎合测试。

Windows沙盒预检已真实执行：联网和剪贴板等关闭，仅映射本任务公开验收程序与输出目录，来宾实际MachineGuid的用途摘要与宿主不同，来宾22621、WDAGUtilityAccount，完成后正常关机。证据 D:/snail-worktrees/commercial-licensing-windows-acceptance-20261023/sandbox-probe-0e3d9a3e92b84f7f8306cfd0513bb949/output/probe.json；此证据只证明独立设备测试环境可用，不证明安装包通过。


### T10 首包实际启动失败与隔离纠正（未通过）

首包5eccbe5已构建，ZIP SHA256 5dc4f6f216beff73efad7dddeef8ae95758c5f4dc5293dcc9ec261044519eb66，仅为待验候选，未交付或发布。包日志发现EDB bin还有stackbuilder.exe，后续改为五个实际命令及DLL白名单，lib仅运行DLL。

首个packaged-offline实际程序验证失败：CDP已打印监听地址但WebSocket连接30秒超时；无参启动也无窗口。主进程及一子进程存在，无Go/PG，资料根仅有window-profile。正常停止也失败，确认准确EXE/PID后终止本任务测试进程，保留原失败；不能归因为缺少等待、RAM或数据库，当前根因未决。后续在源码增加无秘密的stderr阶段标记，定位profile/lock/ready，不放宽超时或断言。

测试隔离错误：Electron Windows app.getPath(appData)按系统壳路径，不跟随APPDATA测试环境变量；本次在真实壳路径 C:/Users/Administrator/AppData/Roaming/SnailLicenseCenter 新建了空window-profile（创建时间04:55:21）。没有runtime.json、数据库、owner、客户或签发身份。该空目录为本次测试残留，记录后按归属清理；不将环境变量伪隔离当成有效隔离。正式管理包后续测试限定真实Windows沙盒，核对WDAGUtilityAccount和实际MachineGuid摘要不同于宿主后才启动，使用来宾真实默认目录；不会触碰宿主正式资料。首个失败证据 C:/Users/Administrator/AppData/Local/Temp/snail-manager-package-idQi5d。

Jev实际jev-1.14.0检查本次失败分流：aligned .99、confidence .98；证据足以归因为等待/RAM .05；整体完成 .02。结果仅帮助核查，非测试通过。


### T10 全新 Windows 运行库缺失定位

0f38eee包在实际沙盒仍未通过，证据 sandbox-manager-8e8dcf567fa2497293affe5cf758e4bd。阶段标记确认单实例锁true、app-ready；c515978进一步确认目录权限及秘密资料根检查通过后抛错，不据此猜测线程/等待原因。

直接执行同包postgres.exe、initdb.exe、pg_ctl.exe --version，三者都返回3221225781（0xC0000135）；来宾实际缺MSVCP140.dll及VCRUNTIME140.dll；PE导入表确认依赖这两项。证据 D:/snail-worktrees/commercial-licensing-windows-acceptance-20261023/sandbox-pg-dependencies-c515978/output/postgres-dependencies.json。这个结果证明全新系统运行依赖缺项，不自动证明所有启动错误只来自同一根因；修复后须回到真实包验证。

修复准备：本机已有VS2022 Community，按微软可再发行目录复制正式x64 CRT 14.44.35211，十个DLL原始签名Valid（Microsoft），固定版本及SHA256进入desktop/native-runtime.lock.json；package.cjs强制要求、校验输入及包内字节，附原微软许可，放数据库程序同目录。不是从System32复制或要求用户安装C++环境。另下载签名有效的新版14.51运行库仅作调查，其最终用户条款与VS分发授权须区分；本包选择VS2022既有正式Redist目录，未采用新版临时提取物。尚未把整改后的正式包判通过。

误落宿主壳目录的本任务空window-profile已核对完整路径、创建时间、无既有资料/链接及无运行进程后清理，原始失败目录中保留清单。其他Windows沙盒、客户端、工作树和资料没有被关闭或清理。


## 2026-10-23 管理端界面整改（原型落实）

用户实际截图暴露原客户页将标题、查询和主操作混在一行、表格黑色边框、空列表仍显示无效分页等问题。此前原生运行及签发用例通过，仅能证明这些动作成立，不能作为界面已完成的依据。

本次改动范围：`web/src/pages/local-offline.tsx`、`local-offline.css`、`local-customers.tsx`、`local-licenses.tsx`、`local-issue.tsx`、`local-signing.tsx`、`local-data-settings.tsx`。以设计包 `界面截图/02_客户管理.png` 为布局依据，实现固定侧栏、页头、独立主操作、列表内工具栏、浅色表头和细边框；客户名称允许换行，空数据不显示无效翻页。档案、授权、交付、设置共用本地主题；长弹窗接入现有 DialogBody，关闭按钮保留在固定区域。未移植原型业务、安全或签发 JS。

密码策略保持原值：10至128个 Unicode 字符，UTF-8不超过512字节。后端 `internal/auth/password.go` 与 `internal/offline/key_store.go` 改为普通中文错误，不再把编码和字节数展示给用户；创建密码输入框只提示“至少10个字符”。正式签发身份、现有资料和权限均未变更。

源码原生验证第一轮：`node --test desktop/local-offline.spec.cjs`，1/1通过，22.83秒。任务隔离根 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-RCQ0cJ`；覆盖首次短密码拒绝且保留商家资料、无邮箱建客户、档案、1120×720窗口、签发下载原字节校验、手动交付、续期换机、备份恢复、同身份移交续期及正常关闭重开。已查看空列表、非空列表、最小窗口、档案、发放表单和签发设置的真实原生截图。后续小幅调整焦点轮廓、文字对比和重复标题后，需再次验证并核对新的实际管理包；本段不证明新包已交付或用户当前窗口已升级。

最终源码界面复核：第二轮同一原生用例1/1通过，23.11秒，隔离根 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-3EFsU9`；此前第一轮证据保留。Go `TestPasswordHash` 与 `TestEncryptedSigningIdentityLifecycle` 各通过。生产前端类型及构建通过（存在既有 bundle 大于500KB提示）。新管理包将另行记录包哈希及沙盒验证，不覆盖原交付目录。

定向前端 lint 未全绿：从根目录调用 Biome 遇到嵌套配置；改在 web 运行后报告6项错误，其中新增原生客户名称按钮缺少显式type已改为button。其余5项为既有刷新/epoch触发依赖被规则认为多余；这些依赖承担重新查询，未采用删除依赖的自动建议。类型构建与真实运行结果另列，不把本检查写成通过。

89fea51实际管理包首次沙盒执行失败于新增验收脚本调用Browser.getWindowForTarget：Electron不提供该CDP方法。此前首次短密码拒绝、两个无邮箱客户、空/非空页面均已通过；停止入口正常关闭。未将此失败称为产品功能通过。包层移除不支持的重复缩放动作，保留默认窗口可达和横向溢出检查；最小窗口的真实原生证据仍归源码层，不冒充包层执行。生产运行文件无改动，复测同一89fea51包并另记验收脚本候选。


界面交付结果：89fea511b17ece1057ed3da829a71890ac1d1d84管理包在干净Windows Sandbox实际1/1通过（17.53秒），验收脚本候选cac2a1a1ee0cc085e172d911bd17cf304f85ee19。覆盖首次短密码拒绝/资料保留、两个无邮箱客户（含长名称换行）、查询和档案、键盘焦点、签发下载原字节验证、下载不记交付、人工登记、run.bat/stop.bat正常停止及重开持久化。已查看实际包列表、档案、设置和完成状态截图；最小窗口仍仅归源码原生验证。实际包和源代码ZIP已复制到 `E:/360Downloads/蜗牛客户与授权中心_界面修正版_89fea51`，增加中文一键启动入口与验收证据。旧交付目录和用户正在运行的窗口未关闭或覆盖，未读取用户私钥/密码/实际客户资料。管理包ZIP SHA256：7226d094006b6acb27bf8afe06fa93cf4149184fe10a73ec47782e193580084c。

独立只读审查：96efd对f781的主要界面无具体行为缺陷，唯一P3为原生button未声明type；89fea整改以及cac2a1a包测适配已分别复审No findings。审查未运行测试，也未替代产品验证。交付为本地便携管理包，未冒充NSIS管理安装；本次UI修正不代表整体商业闭环、正式客户安装器或最终P0已完成。


## 2026-10-23 全部管理界面与实际运行更新（0.1.1）

用户第二次截图以及当前窗口进程证明：之前只生成另一个目录的新包，没有完成原程序所在位置的更新；不能将其当成用户手上界面的完成。原进程来自 `E:/360Downloads/蜗牛客户与授权中心_本地版_f781f4e/管理端/蜗牛客户与授权中心.exe`。本次保留原账号、签发身份与资料，更新范围为可核验的程序文件；实际更新完成与否将在后续记录，不能用生成目录代替。

本轮补齐全部管理页面：概览为四项真实数量、最近发放、30天内到期与待办；客户档案和授权详情将标题/关闭按钮放到滚动区外；授权列表合并客户与授权编号，期限/交付/续发仍分别显示；交付页显示当前原件的交付方式、接收人和时间，正常导航默认全部、概览待办进入明确pending，并可直接下载原件/打开登记；设置按基本信息、签发身份、备份与数据、操作记录分成可键盘切换的页签，保留各表单状态。版本在设置可见，防止再次把运行版本混淆。全局发放入口接入选客户/新建客户后发放，沿用原客户保存及签发API，没有新增安全或签名实现。未造客户或使用遥测填充空界面。

本轮验证：首次原生完整用例失败于旧测试仍查找“待交付 1”，实际按原型改为“待交付文件 1”；仅更新文案消费者，不改数量预期。第二轮1/1通过（25.87秒）；进一步检查交付导航及直接下载后第三轮1/1通过（26.04秒），隔离根 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-9ZLwPE`。保留全部失败日志。覆盖所有主导航空页面和4个设置页签、概览新建客户后发放、已有客户发放、窄窗口和抽屉底部关闭、续期/换机/停发、真实原件验证、备份错误保留及恢复/空实例同身份续期。已查看各主页面及窄抽屉截图。7个直接修改的本地TSX通过Biome check（含格式），类型及生产构建通过；刷新依赖保留并以定向注释说明，不采用删除正确刷新依赖的建议。实际0.1.1管理包和正在使用程序的更新仍需独立记录。


3744017实际0.1.1包的沙盒业务用例1/1通过（18.03秒），但逐图检查发现概览在全局发放弹窗关闭后未刷新：真实授权为1，仍显示旧0。这是该UI路径的实际缺陷，不能凭整条用例通过当界面完成。修正为发放/生命周期结束时把现有epoch传给概览摘要及两张列表；未通过卸载页面、伪造计数或增加轮询掩盖。源码第5轮经概览选客户完成真实签发和人工交付后，立即断言有效1、待交付0及真实客户行，1/1通过（25.21秒），隔离根 `C:\Users\Administrator\AppData\Local\Temp\snail-keygate-t03-IH0pKQ`。包测新增相同结果的消费断言并等待客户行后截图。

主操作可达检查升级为toBeInViewport({ratio:1})，不接受按钮只有一部分可见。源码第4轮1/1通过（24.47秒），独立布局读数记录在Temp/snail-keygate-t03-L5zepi/nav-layout.jsonl：1426px内容窗口中各页主操作右边界1398px，整页scrollWidth1426px，没有越出窗口。当前修正版仍需重新固定包并实际更新原位置。


### 全页面整改的独立审查与状态更新（0.1.1）

3744017/c1e761a 的独立审查发现三项 P2：概览详情内停发/交付未刷新摘要；同名客户选择资料不足；查询失败旧结果仍可选。已由实际成功写操作通知概览与列表刷新；选择器增加真实类型、邮箱、备注及登记时间，签发预览显示联系资料、邮箱、来源及备注；失败和加载期间禁用旧候选，并允许同条件重新查询。未修改签名、安全、台账或备份合同。

`node --test desktop/local-offline.spec.cjs` 最新 1/1 通过（25.30 秒），证据 `C:/Users/Administrator/AppData/Local/Temp/snail-keygate-t03-xmQB0X`，日志 `D:/snail-worktrees/commercial-licensing-windows-acceptance-20261023/manager-all-ui-source-8.log`。新增概览内停发/恢复分别核对 1/0、实际登记交付后待交付归零；正常新建两名同名客户，以邮箱/备注区分，人工注入查询网络失败后旧项全部禁用，同条件重试恢复，再选择乙方核对签发预览不串甲方。首次新增检查因两张概览表均含详情按钮而选择不唯一；第二次在模态框打开时访问被无障碍隔离的背景按钮，均为测试定位错误，已按最近发放区域和关闭后状态修正，未放宽业务预期。

四个直接修改页面 Biome check 与 `VITE_SNAIL_DEPLOYMENT=local_offline VITE_SNAIL_MANAGER_VERSION=0.1.1 bun run build` 通过。此段仍为源码真实 Electron 结果；实际固定包、原路径更新及用户当前程序检查分别补记，不能用另一目录的包替代实际更新。


### 原位置实际更新与管理包验证（9844b41）

固定候选 9844b41eea99cd86fd4361a835c27d21ff0ec019 的四页整改已独立复审 No findings（不代替运行验证）。完整管理构建入口在空输出第一次因 7za 返回 unspecified error 退出 2；同文件单独压缩成功，未确证压缩失败根因。保留原日志，再从新空输出执行正常入口成功，产物 `manager-all-ui-9844b41-r2`，ZIP SHA256 a51c0dca8ec9ab8640c55694b8f3cb5cf11f4b000dff44cb4d169426cc84ebd2。

实际 Windows 沙盒固定包 `node --test desktop/packaged-offline.spec.cjs` 1/1 通过（17.84 秒），证据 `D:/snail-worktrees/commercial-licensing-windows-acceptance-20261023/sandbox-manager-all-ui-9844b41/output/snail-manager-package-Ysq60b`。实际首次设置短密码拒绝保留输入、登录、无邮箱/长名客户、五个页面与四个设置页签、原始签名下载、交付登记、交付列表原件下载字节一致、正常启动/停止 BAT、关闭重开及原件保留通过。管理服务/数据库正常退出；沙盒 guest 正常完成，主机沙盒客户端关闭通知后来无响应，经核对其父 PID 属本次 launcher198812 后，仅清除了已结束测试的客户端 PID291248，见 client-cleanup.json。没有结束其他任务或修改网络安全设置。

本轮已实际正常关闭用户原管理端及 PostgreSQL，按旧包逐文件核对原路径无独有修改后，在 `E:/360Downloads/蜗牛客户与授权中心_本地版_f781f4e/管理端` 只更新 8 个程序文件，并对全部1620文件核对新包哈希。`AppData/Roaming/SnailLicenseCenter` 的 runtime.json 原字节哈希不变，未重置账号、资料或创建替代签发身份；正常资源管理器用户环境重新打开同一原路径，原会话保留。客户、概览、授权、交付四个实际页面已核对新版布局，证据 manager-in-place-upgrade-9844b41；设置页最后巡检暂被另一任务的 Zulu Windows 网络权限弹窗遮挡，未代改其权限，因此不把该页写成本轮原实例已检查。固定包内设置四页签已检查通过。

原文件夹名称保留便于原入口继续使用，内容已更新为0.1.1；根目录补中文 `启动管理端.bat`，交付0.1.1 ZIP、对应源码ZIP、中文操作说明与准确的管理包实际验收.json。前一轮只在新目录复制包没有更新用户原程序，不能视作当时已完成实际更新。本轮仅证明上述管理端工程和实际更新；正式客户安装包、完整客户端测试与P0仍未全部完成。


## 2026-10-24 对抗审查定向整改

起点 f241532，首轮固定候选 a646ef1。R03 恢复确认在原写门禁内逐一比较本机当前签发原件，缺失或冲突拒绝；预览不冻结签发事实，确认时重新比较。R05 登记公钥即约束身份（含零签发、业务包及完整恢复），缺容器返回原公钥/指纹且保持 uncreated，界面显示“缺少原签发身份，请导入”。R06 文件改选立即清数据，读取及预览按代次隔离；保留原错误和输入。R10 保存端口占用时安全重选应用/数据库端口，仅正常停止的任务数据库允许变更端口，配置凭据保留。R12 源码归档按指定提交克隆检出构建，不依赖 ZIP 中不存在的 .git。

新增一次性离线校时修复签发沿原 owner 身份及解锁私钥机制，使用独立 stq-license-recovery/v1 签名域；文件仅绑定申请、设备、安装和状态原件集合，不增加或续期商业许可。合同 contracts/recovery-v1.md，Go/Node 共用 TEST_ONLY.recovery.json 固定测试向量。正式私钥、真实客户与原用户运行目录均未读取或修改。

当前源码原生 Electron 1/1（27.96 秒），包含正常 UI 下载修复文件并用客户端协议验签、原身份缺失指纹展示、身份大文件/坏编码/乱序、较早备份恢复拒绝、最新恢复成功、两个端口占用后重开及凭据不变。Go/Node 桥接首轮 1/1（14.2 秒）。两位独立审查发现的客户端升级和审计提交缺口在客户端整改，管理端缺失身份状态已纠正，待固定整改候选复核。

本记录证明源码验证，不代表新版管理包或正式客户安装器已生成、安装或替换用户当前程序。客户端完整 npm test 本轮在单元层有基线已有 UI 测试约束失败，整体全绿及安装器候选一致性 G01 尚未关闭。


### 2026-10-24 恢复边界复核整改

PR2已合入413604e，随后远端复核指出空身份登记及活动身份损坏两项遗漏。已用实际PostgreSQL新用例在原候选复现：空身份备份/业务包被误判损坏；活动容器截断后可信完整备份无法确认。修正空KeyID/公钥且无历史为未登记状态，null、半缺字段或空登记与签发历史并存仍拒绝。活动身份损坏时仅以可信登记身份及完整原件连续性校验同身份备份，并要求原签发密码实际解锁候选后才能切换；结构完整但密文损坏、错密码、缺替换身份及原件缺失均拒绝。密码只在异常恢复时输入，不写日志、数据库或运行配置。

首次重启任务数据库遗漏原端口，两次测试连接拒绝，属于环境失败；按隔离runtime端口38269重启后才取得功能红绿证据。四项Go定向通过，其中原件连续性、零签发约束保持。初轮新增完整流程因加入前置恢复后仍与最初JWT比较而失败，改为端口占用前快照，只检查端口重选不变更凭据；旧失败保留。另修正恢复密码字段只传恢复API，避免向业务包导入传未知字段，原业务包协议不扩大。bdb8cfe的真实Electron 1/1通过（34.70秒），日志manager-unlock-electron-r2.log，覆盖空备份恢复、首次建身份、损坏容器后缺原签发密码、错密码保留输入及正确密码恢复，随后完成业务包移交、端口重选与关闭重开；独立审查未发现该候选代码问题。

复审进一步指出缺失文件会报告uncreated而非错误。恢复包含已登记原身份的备份时，缺失与损坏活动容器均须实际解锁候选；仍允许不带身份的备份保留已登记公钥、等待后来导入原身份，不允许改建其他身份。新增missing子场景在bdb8cfe失败，修复后与corrupt子场景共同通过；两者均覆盖缺密码、错密码、同身份正常恢复后继续r2及结构有效但GCM密文不可解的拒绝，源库原件仍为r1。

最终复测使用已运行的隔离PostgreSQL。`go test ./internal/service -run '^TestOfflineRestoreRepairsCorruptActiveContainerFromRegisteredIdentity$' -count=1`通过（5.813秒）；恢复相关两项组合通过（9.000秒）。重新`go build -o bin/snail-license-center.exe ./cmd/server`后，`SNAIL_CLIENT_ROOT=<本任务客户端工作树> SNAIL_MANAGER_TEST_POSTGRES=<已核验PostgreSQL> node --test desktop/local-offline.spec.cjs`再次1/1通过（31.41秒），证据为Temp/snail-keygate-t03-61ag04。完整日志manager-missing-red.log、manager-missing-green.log、manager-recovery-final.log和manager-missing-electron-final.log收录于本轮交付证据；源码提交身份及最终独立复审结果在交付回执记录。没有生成正式新包、读取正式私钥或替换用户原程序。

## 2026-10-29 授权码升级

当前产品候选 `e18b088924bbb2815f697d3d1309c5160786ca09` 基于 `07499ee6391c911184d412fbf32442a31e086772`，分支 `codex/authorization-center-20261029`，草稿 [PR5](https://github.com/maple192600-LI/keygate/pull/5)。客户端对应[PR159](https://github.com/maple192600-LI/snail-finance/pull/159)。当前没有域名，仅预留接口及配置；历史交付记录不能替代本轮验收。

本轮改为原授权码激活/同步、SMBIOS UUID 摘要、完整购买期限和永久、签名设备转出；恢复概览、客户、授权、交付记录及设置，保留新增即将到期，使用统一蓝鸟图标。账号、账套权限及会计规则未改变。既有 `license_deliveries` 增加授权关联并回填，授权码交付允许无签发原件；同表保存历史、审计与幂等。存在码交付时拒绝向下迁移，部署恢复使用完整加密备份，不能为降 Schema 删除事实。

相关 Go 核心/HTTP、交付跨激活续期、历史回填、Bundle/FullBackup 和指定日期定向场景通过；旧整体五包运行曾失败，不能拼成一次五包全绿。最终期限及真实 Node 双向互验 5/5 通过。管理 Electron 的登录启动场景 1/1（5.4秒）和主授权场景 1/1（19.9秒，0 skipped）分别通过。主场景实际执行导航、发放恢复、明码显示/系统剪贴板替换、交付历史/筛选、续费换机、错密拒绝/正确备份恢复、移交账号隔离和重开停服。新增图标在新隔离初始窗口实际核对，未创建账号或客户。各受影响候选独立复审无未处理发现。

直接派生长期后台子进程曾使 Electron 的退出事件完成后仍不退出；当前由既有 Windows PowerShell 交接独立后台，保留健康身份验证与正式停止入口。实际关窗退出、后台和PG继续可用、重开复用同一宿主，以及最后正常停机已验证，没有以强退修复产品。

两个独立本机服务/数据库实例通过完整加密备份迁移并保留相同签发身份、原码和期限；先暂停入口、停止源写入，再恢复及切换，没有双写。真实小团雀沿原入口取得目标续费修订；原生替换取消/确认、永久重开、签名转出和断开连接后重开只读均通过。转出证据是正常启动同步及离线保存，不是实时撤销。

Go产物 SHA-256：`1D054FCB74F17D11372C572FB58457B906272BA2900543814C46D6DEBADE68BA`。本轮未生成或安装正式客户包，未实际重装Windows，未部署公网或发布，不宣称全部18项验收完成。所有合成服务、数据库及客户进程已停止，19个隔离根、任务链接、测试凭据和无用缓存已清理；一次工具截图包含的合成码已通过原入口停发并验证拒绝激活。未替换现用安装或写入真实账套。未合并源码、必要原始证据及构建工具保留用于草稿审查。
## 2026-10-30 首轮实际安装接续（后续结果见末节）

用户纠正名称为“小团雀”，并明确要求更新桌面客户端及办理本机授权。管理端当前可见名称、窗口、运行及停止脚本、包名和操作说明均统一为“小团雀客户授权中心”，保留原应用标识、资料路径、启动项键、六模块、蓝鸟图标与 Powered by Keygate。

固定产品候选 `577f4cd9862b6cbca02b1086783636c94b07c6f0`，使用 Go 1.27.3、Bun 1.4.4、Node 24.18.0 构建；在干净候选完成管理 desktop 的 `npm ci` 及固定打包入口。管理 ZIP SHA-256 为 `a0a2873a2d09ea314a3e10d710b65338794455a8ab9c680889a6146b33a6ba51`，对应源码 ZIP SHA-256 为 `47e507029709c8c8c8d775ebc70423b440f2c83f06c8339a4b50dae328717468`。增量语法、Biome 及独立只读复审通过。

现用程序正常关闭、后台与数据库退出后，在原目录更新；1,625 个包文件逐项 SHA-256 一致。桌面快捷方式更名为“小团雀授权管理”，正常登录后台启动项指向新程序。真实窗口已打开并确认名称、图标、六模块、原登录会话和原合成验收客户保留；原签发身份及 runtime 配置摘要与升级前一致，本机客户接口已监听 127.0.0.1:9100。

财务客户端形成 required/commercial 正式 NSIS，仍信任同一原厂公钥，构建时固定本机回环入口；外部 HTTPS 仅预留，不要求域名。实际安装向导已识别原路径，但 UI 工具对安装和取消均未使其继续，未断言具体原因，未强杀或绕过权限。当前等待本机安装操作和期限决定；签发身份迁移需用户在本机输入原密码，未新建替代身份、未签发本机许可。受限迁移恢复点保留至实际升级与授权验证完成；未合并或发布。

## 2026-10-30 当前交付与身份恢复核对

用户最终指定两端使用财务软件原小团雀图标；客户档案、授权详情、客户选择/新建、发放、续期及换机均改为现有主内容页，取消对应抽屉和模态表单，保留六模块、字段和动作。列表保持挂载隐藏，返回保留来源、查询和筛选。原 ICO 与财务基线原字节一致，Web SVG 无损嵌入同一原 PNG。

独立复审指出页面化后侧栏可能丢弃未决操作，已通过现有 busy/uncertain 状态保护导航与退出；退出请求本身也在等待期间通过原生 inert 保护既有工作区。没有新增审批或持久化机制。最终产品候选 `81ba36a73e02d1ddc4cb00643e5b2d226f777960` 的定向真实 Electron 单场景 1/1、无跳过、21.05 秒，涵盖主内容详情无对话框、查询及交付筛选返回保持、同操作号恢复、退出等待及正常关闭重开。新增退出测试首轮路由解除与继续请求不同步失败，已保留原始失败，只修复等待和 inert 控件只读观察；不能拼写为完整测试全绿。产品及增量测试独立复审无发现。

最终 ZIP SHA-256 为 `5f69da4d42fc5927117a3ae559254f5506f95c3b1e752ae8d9cebe09eda15ac2`，对应源码 ZIP SHA-256 为 `05cc7e8c054e82aebada8a07b117355c8e002db1e88c8353010c9f80600871a5`。旧正式实例通过自身停止入口正常关闭后原位更新，1,625 个包文件一致。实际窗口正常重开、原登录及六模块保留；部分 Web 控件受电脑操作工具定位限制，未把接口与源代码定向测试当作正式实例全部点击验收。临时回环调试仅用于复用原会话的只读身份 GET，结束后正常停启并确认调试入口关闭。

原身份接口返回 locked 且已有历史；用户忘记原签发密码。已找到的原加密身份备份与现用公钥和密文相同，恢复入口仍需同一密码；现有业务包只有公开身份及业务表。原管理库为 1 个合成验收客户、1 份授权、3 次签发与 1 次交付。用户现已明确同意保留原库并建立新自动签发身份、更新客户包信任公钥；接续结果见下节。财务软件本身已用恢复原图标的正式包完成原位升级，原财务账号与账套保留。

被替代的 4 个本任务包目录、5 个测试资料根及 PostgreSQL 测试链接已清理，必要原始日志和最终包保留；原管理资料、原 Documents 备份、正式数据库程序及受限升级恢复副本保持。PID 复用按当前进程身份核对，未结束无关进程。原密码输入辅助进程在用户表示忘记后停止并清理，未收到密码或产生凭据文件。分支和工作树继续保留，未合并、未发布。

## 2026-10-30 用户同意新身份后的实际接续

管理端经正常停止入口关闭，窗口进程、后台状态及管理/客户监听均核对已停止。原管理资料移入用户确认的独立保留目录，原 Documents 备份不动；1,546 个身份、运行配置和数据库文件与移动前清单的 SHA-256 一致。首轮目录移动只完成部分，已按明确子项完成并核对；剩余窗口档案另行保留，默认路径核对为零文件。自动审批拒绝过含空目录删除的组合命令，后续保留空目录而未执行该删除。

当前正式管理程序已正常启动新库，原 setup/status 返回 needed=true；界面工具未能读取首次设置字段，不能据此宣称 UI 设置完成。现已准备通过正常首次设置、密码登录和发放 API 继续，待用户在本机辅助窗口输入新管理密码和既有财务超级管理员密码；仅保存当前 Windows 用户保护的 DPAPI 密文。未要求原签发密码。当前尚未创建新签发身份、签发本机一年码或重包新公钥客户端，保留 Draft 和所需接续材料。
