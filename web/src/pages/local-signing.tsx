import { type FormEvent, useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { downloadBlob, localDownload, localRequest, readLocalFile } from "@/lib/local-offline-api"

export type SigningIdentity = {
  keyId: string
  publicKeyPem: string
  state: "uncreated" | "locked" | "ready"
  createdAt: string
}
export default function LocalSigningIdentity() {
  const [identity, setIdentity] = useState<SigningIdentity | null>(null),
    [hasHistory, setHistory] = useState(false)
  const [mode, setMode] = useState<"create" | "import">("create"),
    [ownerPassword, setOwnerPassword] = useState(""),
    [passphrase, setPassphrase] = useState(""),
    [confirm, setConfirm] = useState("")
  const [identityFile, setFile] = useState(""),
    [preview, setPreview] = useState<{ previewId: string; keyId: string; matchesHistory: boolean } | null>(null)
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("")
  const [repairFile, setRepairFile] = useState<File | null>(null)
  const fileGeneration = useRef(0)
  const [readingFile, setReadingFile] = useState(false)
  async function refresh() {
    const result = await localRequest<{ identity: SigningIdentity; hasHistory: boolean }>("/admin/offline-key/public")
    setIdentity(result.identity)
    setHistory(result.hasHistory)
    if (result.hasHistory && result.identity.state === "uncreated") setMode("import")
  }
  useEffect(() => {
    let alive = true
    localRequest<{ identity: SigningIdentity; hasHistory: boolean }>("/admin/offline-key/public")
      .then((result) => {
        if (alive) {
          setIdentity(result.identity)
          setHistory(result.hasHistory)
          if (result.hasHistory && result.identity.state === "uncreated") setMode("import")
        }
      })
      .catch((cause) => {
        if (alive) setError(cause.message)
      })
    return () => {
      fileGeneration.current++
      alive = false
    }
  }, [])
  async function perform(action: () => Promise<void>) {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      await action()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "操作未完成，请重新检查")
    } finally {
      setBusy(false)
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault()
    await perform(async () => {
      if (identity?.state === "locked") await localRequest("/admin/offline-key/unlock", { passphrase })
      else if (mode === "create") {
        if (passphrase !== confirm) throw new Error("两次签发密码输入不一致")
        await localRequest("/admin/offline-key/create", { ownerPassword, passphrase })
      } else if (!preview) {
        if (readingFile || !identityFile) throw new Error("请先选择并完整读取签发身份文件")
        const generation = fileGeneration.current
        const result = await localRequest<{ previewId: string; keyId: string; matchesHistory: boolean }>(
          "/admin/offline-key/import/preview",
          { identityFile, passphrase },
        )
        if (generation !== fileGeneration.current) return
        setPreview(result)
        setPassphrase("")
        return
      } else await localRequest("/admin/offline-key/import", { previewId: preview.previewId, ownerPassword })
      setOwnerPassword("")
      setPassphrase("")
      setConfirm("")
      setFile("")
      setPreview(null)
      await refresh()
    })
  }
  const uncreated = identity?.state === "uncreated",
    importing = mode === "import" && uncreated
  return (
    <section className="grid gap-5 rounded-xl border bg-card p-6" data-testid="signing-identity">
      <div className="flex items-center gap-3">
        <span className="lc-signing-status" role="status">
          {hasHistory && uncreated
            ? "缺少原签发身份，请导入"
            : identity
              ? { uncreated: "未创建", locked: "已锁定", ready: "已就绪" }[identity.state]
              : "正在读取"}
        </span>
        <Button variant="outline" disabled={busy} onClick={() => void perform(refresh)}>
          刷新
        </Button>
      </div>
      {error && (
        <div role="alert" className="text-sm text-destructive">
          {error}
        </div>
      )}
      {identity?.keyId && (
        <dl className="grid grid-cols-[7rem_1fr] gap-2 text-sm">
          <dt>身份指纹</dt>
          <dd className="break-all">{identity.keyId}</dd>
        </dl>
      )}
      {identity?.state === "ready" && (
        <div className="flex gap-3">
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              void perform(async () => {
                await localRequest("/admin/offline-key/lock", {})
                await refresh()
              })
            }
          >
            锁定签发
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              downloadBlob(
                new Blob(
                  [
                    JSON.stringify(
                      {
                        formatVersion: 1,
                        productId: "cn.snailfinance.desktop",
                        purpose: "commercial",
                        authorizationMode: "required",
                        licenseKey: { keyId: identity.keyId, publicKeyPem: identity.publicKeyPem },
                      },
                      null,
                      2,
                    ),
                  ],
                  { type: "application/json" },
                ),
                "蜗牛财务发行公钥.json",
              )
            }
          >
            导出发行公钥
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() =>
              void perform(() => localDownload("/admin/offline-key/encrypted", "蜗牛签发身份.encrypted.json"))
            }
          >
            备份签发身份
          </Button>
        </div>
      )}
      {identity?.state !== "ready" && identity && (
        <>
          {uncreated && (
            <div className="flex gap-3">
              <Button
                variant={mode === "create" ? "default" : "outline"}
                disabled={busy || hasHistory}
                onClick={() => {
                  setMode("create")
                  setPreview(null)
                }}
              >
                创建签发身份
              </Button>
              <Button
                variant={mode === "import" ? "default" : "outline"}
                disabled={busy}
                onClick={() => {
                  setMode("import")
                  setPreview(null)
                }}
              >
                导入已有身份
              </Button>
            </div>
          )}
          <form className="grid max-w-lg grid-cols-2 gap-4" onSubmit={submit}>
            {importing && !preview && (
              <div className="col-span-2 grid gap-2">
                <Label htmlFor="signing-file">加密签发身份文件</Label>
                <Input
                  id="signing-file"
                  type="file"
                  accept=".json"
                  disabled={busy}
                  onChange={async (event) => {
                    const file = event.target.files?.[0]
                    const generation = ++fileGeneration.current
                    setFile("")
                    setPreview(null)
                    setError("")
                    setReadingFile(Boolean(file))
                    try {
                      const raw = file ? await readLocalFile(file, 65536) : ""
                      if (generation === fileGeneration.current) setFile(raw)
                    } catch (cause) {
                      if (generation === fileGeneration.current) setError((cause as Error).message)
                    } finally {
                      if (generation === fileGeneration.current) setReadingFile(false)
                    }
                  }}
                  required
                />
              </div>
            )}
            {preview ? (
              <div className="col-span-2 break-all text-sm">
                待导入身份：{preview.keyId} · {preview.matchesHistory ? "历史匹配" : "历史不匹配"}
              </div>
            ) : (
              <div className="grid gap-2">
                <Label htmlFor="signing-passphrase">签发密码</Label>
                <Input
                  id="signing-passphrase"
                  placeholder={uncreated && mode === "create" ? "至少10个字符" : undefined}
                  type="password"
                  autoComplete="off"
                  value={passphrase}
                  onChange={(event) => setPassphrase(event.target.value)}
                  disabled={busy}
                  required
                />
              </div>
            )}
            {uncreated && mode === "create" && (
              <div className="grid gap-2">
                <Label htmlFor="signing-confirm">确认签发密码</Label>
                <Input
                  id="signing-confirm"
                  type="password"
                  autoComplete="off"
                  value={confirm}
                  onChange={(event) => setConfirm(event.target.value)}
                  disabled={busy}
                  required
                />
              </div>
            )}
            {uncreated && (!importing || preview) && (
              <div className="grid gap-2">
                <Label htmlFor="signing-owner-password">管理密码</Label>
                <Input
                  id="signing-owner-password"
                  type="password"
                  autoComplete="current-password"
                  value={ownerPassword}
                  onChange={(event) => setOwnerPassword(event.target.value)}
                  disabled={busy}
                  required
                />
              </div>
            )}
            <div className="col-span-2">
              <Button
                type="submit"
                disabled={
                  busy ||
                  (importing && !preview && (readingFile || !identityFile)) ||
                  (!uncreated && identity.state !== "locked")
                }
              >
                {busy
                  ? "正在处理…"
                  : identity.state === "locked"
                    ? "解锁签发"
                    : mode === "create"
                      ? "确认创建签发身份"
                      : preview
                        ? "确认导入身份"
                        : "预览签发身份"}
              </Button>
            </div>
          </form>
        </>
      )}
      {identity?.state === "ready" && (
        <details>
          <summary className="cursor-pointer text-sm">处理客户授权故障</summary>
          <form
            className="grid max-w-lg grid-cols-2 gap-4"
            onSubmit={(event) => {
              event.preventDefault()
              void perform(async () => {
                if (!repairFile) throw new Error("请选择修复申请")
                const requestFile = await readLocalFile(repairFile, 65536)
                await localDownload("/admin/offline-key/recovery", "蜗牛授权修复.snailrepair", {
                  requestFile,
                })
              })
            }}
          >
            <div className="grid gap-2">
              <Label htmlFor="repair-request">修复申请</Label>
              <Input
                id="repair-request"
                type="file"
                accept=".snailreq"
                disabled={busy}
                required
                onChange={(event) => setRepairFile(event.target.files?.[0] || null)}
              />
            </div>
            <div className="col-span-2">
              <Button type="submit" disabled={busy || !repairFile}>
                生成并保存修复文件
              </Button>
            </div>
          </form>
        </details>
      )}
    </section>
  )
}
