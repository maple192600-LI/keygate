# 蜗牛 v2 开发合同附件

2026-10-29 当前统一合同：设备类型为 `windows-smbios-uuid-sha256/v1`；签名载荷固定在 `binding` 与 `keyId` 之间包含必填 `bindingState`（`active` 或 `transferred`）。厂商换出决定经同设备更高修订的签名原件同步，保留原真实购买期限和永久空截止；普通网络错误不能撤销本机许可。固定 TEST_ONLY 向量已按当前合同重新签名。历史迁移仅保留此前原始字节，不增加运行时双协议兼容。以下早期附件说明不代替本轮源码及验收结果。

本目录是《01_蜗牛授权管理_开发实施方案.md》第 7 节和第 13 节的机器可读附件。用于直接开发、生成类型和编写验收，不是已经实现的授权服务。

## 文件

| 文件 | 用途 |
|---|---|
| request-v2.schema.json | .snailreq 申请结构；UUID v4；设备摘要与描述字段 |
| license-envelope-v2.schema.json | .snaillic 外层四字段 |
| license-payload-v2.schema.json | 已验签载荷的结构；字段顺序与主方案一致 |
| term-cases.json | 第 13.2 节八个固定日期预期，以及截止边界检查点 |
| TEST_ONLY.request-v2.snailreq | 合成申请示例 |
| TEST_ONLY.license-v2.snaillic | 固定、已签名的开发样例 |
| TEST_ONLY.public-key.pem | 上述开发样例的公钥 |
| TEST_ONLY.vector.json | 固定身份、载荷、文件哈希及预期签名 |
| verify-test-vector.cjs | 只用公钥独立复核附件向量的 Node 脚本 |

本次合同只含实际使用的字段。没有 subjectUserId、安装 UUID、perpetual、features 或自带可信公钥。永久统一为 expiresAt = null。

## Schema 的责任

三个 Schema 均采用 JSON Schema 2020-12；所有对象都显式 additionalProperties:false，所有正文固定字段均为必填。产品代码保留为字符串，由运行时和受保护发行策略核对；蜗牛财务的产品代码是 cn.snailfinance.desktop，不能仅凭 Schema 通过就认定产品正确。

时间字符串固定为毫秒 UTC，例如 2026-10-22T01:00:00.000Z。正则限制字面形式，date-time 格式约束是否执行还取决于验证器设置。实现须检查真实日期、无时区漂移、精度和往返结果，拒绝 2 月 30 日等无效日期。有限 expiresAt 必须大于 notBefore。

JSON Schema 不替代以下责任：

1. 在解析前检查文件字节数、严格 UTF-8 和嵌套深度；申请上限 64 KiB，许可证上限 128 KiB，深度上限 8。
2. 解析器拒绝重复键，包括转义后同名字段；普通 JSON.parse 和先解析再 Schema 校验都无法单独完成这一要求。
3. base64url 解码再编码比较，拒绝非规范编码，并核对签名为 64 字节。
4. 使用内置信任列表中的 Ed25519 公钥，对原始 payload 字节和固定签名域验签。
5. 验签后再检查载荷，核对内外 keyId、预期产品、commercial、设备、真实时间、期限与版本。
6. 使用本机受控读取的设备摘要。申请中的标签、版本和设备码均不能自行提升权限。
7. 执行同原件幂等、版本防回退、无效文件不覆盖有效状态及首次管理员创建门禁。
8. 保存待生效文件不能创建管理员或开放业务，只有当其已生效并通过全部检查才允许继续。

签发端按 license-payload-v2.schema.json 的 properties 顺序输出紧凑 UTF-8 JSON，binding 内顺序为 type、digest，无 BOM、无尾换行。Schema 本身不检查顺序。验签端应验证收到的原字节，不先按该顺序重排再验签。

签名消息为 UTF8("stq-license/v2") + 0x00 + 原始载荷字节。keyId 为 lic- 加 SHA256(UTF8("stq-license-key/v1") + 0x00 + SPKI DER 公钥字节) 的小写十六进制值。两个版本号属于不同合同，不需要强制相同。

## 期限预期

term-cases.json 的 expected 是按主规格独立填写的固定答案，不是运行待测期限函数后反写的结果。input 是测试资料结构，不是新增加的管理 API。

所有显示时间以 Asia/Shanghai 解释；落盘用 UTC 毫秒。天数按 N×24 小时；自然月按当地日历夹紧月底；指定截止日包含当天，内部截止为次日零点；续期从 max(签发时刻, 原截止) 延长，新 notBefore 按主规格单独确定。

第 13.2 节“10-22 续一个月”未写当天具体时刻，本附件将该用例的签发时刻明确为北京时间 09:00；从原到期时刻延长后的截止答案保持原表一致。

边界断言只评估时间条件，假定其他签名、产品、设备和许可状态校验均已通过。永久仍须遵守 notBefore，不代表尚未生效时即可使用。

## TEST_ONLY 向量

样例使用 [RFC 8032 第 7.1 节 TEST 1](https://www.rfc-editor.org/rfc/rfc8032.html#section-7.1) 的公开测试身份生成。目录只交付公钥、签名和原件，不交付私钥、seed 或签发脚本。设备摘要和客户标识均为合成数据。

TEST_ONLY 身份禁止进入正式发行信任列表，也不能用作厂商长期签发身份。样例载荷中的 purpose 保持 commercial，是为了准确覆盖正式字段结构；文件名、说明及向量元数据明确标记其仅供开发。正式包校验须排除本目录的测试身份和测试许可证。

在本目录执行：

```powershell
node verify-test-vector.cjs
```

该脚本只读取公钥复核原始字节签名、keyId、文件摘要、规范编码和字段顺序，并确认改一个字节或更换签名域会验签失败。它不包含私钥。它也不是严格生产解析器，不能拿脚本中普通 JSON.parse 直接替代产品协议解析器。

本次已完成 Node 公钥独立验签和结构附件检查。Go 端还没有实现，Go/Node 双向互验、产品初始化门禁和 Windows 安装验收均须在开发任务 T02/T06/T10 中完成，不能把附件验证计为产品交付通过。

