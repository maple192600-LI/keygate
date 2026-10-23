import { type FormEvent, useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { localDownload, localFileRequest, localRequest } from "@/lib/local-offline-api"

type Summary = {
  customers: number
  licenses: number
  originals: number
  keyId: string
  mappings: { kind: string; code: string; sourceId: string; targetId: string }[]
}
type Preview = { previewId: string; summary: Summary; includesIdentity?: boolean }
type Operation = { id: string; entity: string; action: string; actor: string; createdAt: string }
const message = (e: unknown) => (e instanceof Error ? e.message : "操作未完成，请重试")
const entities: Record<string, string> = {
  "commercial-customer": "客户",
  "offline-customer": "客户",
  "offline-request": "设备申请",
  "offline-license": "授权",
  "license-issuance": "签发",
  "license-delivery": "交付",
  "offline-signing-identity": "签发身份",
  session: "登录会话",
  owner: "管理账号",
  merchant: "商家资料",
  "business-bundle": "管理数据",
  backup: "备份",
}
const actions: Record<string, string> = {
  create: "新建",
  update: "修改",
  import: "导入",
  initialize: "首次设置",
  login: "登录",
  logout: "退出登录",
  "change-password": "修改密码",
  unlock: "解锁",
  lock: "锁定",
  initial: "首次签发",
  renew: "续期",
  transfer: "换机",
  record: "登记交付",
  stop: "停发",
  resume: "恢复续发",
  restore: "恢复资料",
  "issuance-state": "调整续发状态",
}

export function MerchantSettings() {
  const [siteName, setName] = useState(""),
    [contact, setContact] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false),
    [ready, setReady] = useState(false)
  useEffect(() => {
    let alive = true
    localRequest<{ siteName: string; contact: string }>("/admin/local-settings")
      .then((v) => {
        if (alive) {
          setName(v.siteName)
          setContact(v.contact)
          setReady(true)
        }
      })
      .catch((e) => {
        if (alive) setError(message(e))
      })
    return () => {
      alive = false
    }
  }, [])
  async function submit(e: FormEvent) {
    e.preventDefault()
    if (busy || !ready) return
    setBusy(true)
    setError("")
    setSaved(false)
    try {
      await localRequest("/admin/local-settings", { siteName, contact }, "PATCH")
      setSaved(true)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="max-w-lg rounded-xl border bg-card p-6">
      <h2 className="mb-6 text-lg font-semibold">商家资料</h2>
      <form className="grid gap-4" onSubmit={submit}>
        <div className="grid gap-2">
          <Label htmlFor="merchant-name">商家名称</Label>
          <Input
            id="merchant-name"
            required
            value={siteName}
            disabled={busy || !ready}
            onChange={(e) => {
              setName(e.target.value)
              setSaved(false)
            }}
          />
        </div>
        <div className="grid gap-2">
          <Label htmlFor="merchant-contact">联系方式</Label>
          <Input
            id="merchant-contact"
            required
            value={contact}
            disabled={busy || !ready}
            onChange={(e) => {
              setContact(e.target.value)
              setSaved(false)
            }}
          />
        </div>
        {error && (
          <div role="alert" className="text-destructive">
            {error}
          </div>
        )}
        {saved && <div role="status">已保存</div>}
        <Button className="justify-self-start" disabled={busy || !ready}>
          保存商家资料
        </Button>
      </form>
    </section>
  )
}
export default function LocalDataSettings({ onImported }: { onImported: () => void }) {
  const [mode, setMode] = useState<"" | "backup" | "bundle" | "restore">(""),
    [file, setFile] = useState<File | null>(null),
    [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState(""),
    [ownerPassword, setOwner] = useState(""),
    [include, setInclude] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("")
  async function perform(action: () => Promise<void>) {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      await action()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function close() {
    if (busy) return
    if (preview) {
      await perform(async () => {
        await localRequest("/admin/data-previews/cancel", { previewId: preview.previewId })
        reset()
      })
    } else reset()
  }
  function reset() {
    setMode("")
    setFile(null)
    setPassword("")
    setConfirm("")
    setOwner("")
    setInclude(false)
    setPreview(null)
    setError("")
  }
  async function submit(e: FormEvent) {
    e.preventDefault()
    await perform(async () => {
      if (mode === "backup") {
        if (password !== confirm) throw new Error("两次备份密码输入不一致")
        if (include && !window.confirm("备份将包含加密签发身份。请只保存在厂商控制的位置，是否继续？")) return
        await localDownload("/admin/backups", "蜗牛管理资料.snailbackup", {
          password,
          includeIdentity: include,
          confirmIdentity: include,
        })
        setNotice("备份文件已生成")
        reset()
        return
      }
      if (!preview) {
        if (!file) throw new Error("请选择完整文件")
        setPreview(
          await localFileRequest<Preview>(
            mode === "bundle" ? "/admin/business-bundle/preview" : "/admin/backups/preview",
            file,
            mode === "restore" ? password : undefined,
          ),
        )
        setPassword("")
        return
      }
      await localRequest(mode === "bundle" ? "/admin/business-bundle/import" : "/admin/backups/restore", {
        previewId: preview.previewId,
        ownerPassword,
      })
      if (mode === "restore") {
        window.location.reload()
        return
      }
      setNotice("管理数据已导入")
      reset()
      onImported()
    })
  }
  return (
    <section className="grid gap-5 rounded-xl border bg-card p-6" data-testid="data-settings">
      <h2 className="text-lg font-semibold">备份与移交</h2>
      <div className="flex flex-wrap gap-3">
        <Button
          disabled={busy}
          onClick={() => {
            setNotice("")
            setMode("backup")
          }}
        >
          备份资料
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() =>
            void perform(async () => {
              await localDownload("/admin/business-bundle/export", "蜗牛管理数据.snailbundle", {})
              setNotice("管理数据包已生成")
            })
          }
        >
          导出管理数据
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            setNotice("")
            setMode("bundle")
          }}
        >
          导入管理数据
        </Button>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => {
            setNotice("")
            setMode("restore")
          }}
        >
          恢复备份
        </Button>
      </div>
      {!mode && error && (
        <div role="alert" className="text-destructive">
          {error}
        </div>
      )}
      {notice && <div role="status">{notice}</div>}
      <Dialog
        open={mode !== ""}
        onOpenChange={(open) => {
          if (!open) void close()
        }}
      >
        <DialogContent
          className="lc-dialog sm:max-w-2xl"
          onInteractOutside={(event) => event.preventDefault()}
          onEscapeKeyDown={(event) => {
            if (busy) event.preventDefault()
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {mode === "backup" ? "备份资料" : mode === "bundle" ? "导入管理数据" : "恢复备份"}
            </DialogTitle>
          </DialogHeader>
          <DialogBody>
            <form className="grid gap-5" onSubmit={submit}>
              {mode !== "backup" && !preview && (
                <div className="grid gap-2">
                  <Label htmlFor="management-file">{mode === "bundle" ? "管理数据包" : "加密备份文件"}</Label>
                  <Input
                    id="management-file"
                    type="file"
                    accept={mode === "bundle" ? ".snailbundle" : ".snailbackup"}
                    required
                    disabled={busy}
                    onChange={(e) => setFile(e.target.files?.[0] || null)}
                  />
                </div>
              )}
              {(mode === "backup" || (mode === "restore" && !preview)) && (
                <div className="grid gap-2">
                  <Label htmlFor="backup-password">备份密码</Label>
                  <Input
                    id="backup-password"
                    type="password"
                    value={password}
                    autoComplete="off"
                    disabled={busy}
                    required
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </div>
              )}
              {mode === "backup" && (
                <>
                  <div className="grid gap-2">
                    <Label htmlFor="backup-confirm">确认备份密码</Label>
                    <Input
                      id="backup-confirm"
                      type="password"
                      value={confirm}
                      autoComplete="off"
                      disabled={busy}
                      required
                      onChange={(e) => setConfirm(e.target.value)}
                    />
                  </div>
                  <Label className="flex items-center gap-3">
                    <input
                      type="checkbox"
                      checked={include}
                      disabled={busy}
                      onChange={(e) => setInclude(e.target.checked)}
                    />
                    包含加密签发身份
                  </Label>
                </>
              )}
              {preview && (
                <>
                  <dl className="grid grid-cols-2 gap-3 rounded-lg border p-4">
                    <dt>客户</dt>
                    <dd>{preview.summary.customers}</dd>
                    <dt>授权</dt>
                    <dd>{preview.summary.licenses}</dd>
                    <dt>授权原件</dt>
                    <dd>{preview.summary.originals}</dd>
                    <dt>签发身份</dt>
                    <dd className="break-all">{preview.summary.keyId || "未创建"}</dd>
                    {mode === "restore" && (
                      <>
                        <dt>加密签发身份</dt>
                        <dd>{preview.includesIdentity ? "已包含" : "未包含"}</dd>
                      </>
                    )}
                  </dl>
                  {preview.summary.mappings.length > 0 && (
                    <details className="rounded-lg border p-4">
                      <summary className="cursor-pointer">产品与计划映射</summary>
                      <table className="mt-3 w-full text-left text-sm">
                        <thead>
                          <tr>
                            <th>合同代码</th>
                            <th>来源</th>
                            <th>目标</th>
                          </tr>
                        </thead>
                        <tbody>
                          {preview.summary.mappings.map((row) => (
                            <tr key={`${row.kind}-${row.sourceId}`}>
                              <td className="p-2">{row.code}</td>
                              <td className="break-all p-2">{row.sourceId}</td>
                              <td className="break-all p-2">{row.targetId}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </details>
                  )}
                  <div className="grid gap-2">
                    <Label htmlFor="restore-owner-password">当前管理密码</Label>
                    <Input
                      id="restore-owner-password"
                      type="password"
                      value={ownerPassword}
                      autoComplete="current-password"
                      disabled={busy}
                      required
                      onChange={(e) => setOwner(e.target.value)}
                    />
                  </div>
                </>
              )}
              {error && (
                <div role="alert" className="text-destructive">
                  {error}
                </div>
              )}
              <div className="flex justify-end gap-3">
                <Button type="button" variant="outline" disabled={busy} onClick={() => void close()}>
                  取消
                </Button>
                <Button disabled={busy} type="submit">
                  {busy
                    ? "正在处理…"
                    : mode === "backup"
                      ? "生成备份"
                      : !preview
                        ? "验证并预览"
                        : mode === "bundle"
                          ? "确认导入管理数据"
                          : "确认恢复并重新登录"}
                </Button>
              </div>
            </form>
          </DialogBody>
        </DialogContent>
      </Dialog>
    </section>
  )
}
export function LocalOperationHistory({ epoch }: { epoch: number }) {
  const [items, setItems] = useState<Operation[]>([]),
    [error, setError] = useState("")
  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch refreshes the recorded operation list after an import.
  useEffect(() => {
    let alive = true
    setError("")
    localRequest<Operation[]>("/admin/local-operations")
      .then((rows) => {
        if (alive) setItems(rows)
      })
      .catch((e) => {
        if (alive) setError(message(e))
      })
    return () => {
      alive = false
    }
  }, [epoch])
  return (
    <section className="rounded-xl border bg-card p-6">
      <h2 className="text-lg font-semibold">操作记录</h2>
      {error && (
        <div role="alert" className="text-destructive">
          {error}
        </div>
      )}
      <table className="mt-4 w-full text-left text-sm">
        <thead>
          <tr>
            <th className="p-2">时间</th>
            <th className="p-2">对象</th>
            <th className="p-2">操作</th>
            <th className="p-2">操作者</th>
          </tr>
        </thead>
        <tbody>
          {items.map((row) => (
            <tr key={row.id} className="border-t">
              <td className="p-2">{new Date(row.createdAt).toLocaleString("zh-CN", { hour12: false })}</td>
              <td className="p-2">{entities[row.entity] || "管理资料"}</td>
              <td className="p-2">{actions[row.action] || "其他操作"}</td>
              <td className="p-2">{row.actor}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  )
}
