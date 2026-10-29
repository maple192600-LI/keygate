import { type FormEvent, useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { downloadBlob, localRequest } from "@/lib/local-offline-api"

type SigningIdentity = {
  keyId: string
  publicKeyPem: string
  state: "uncreated" | "locked" | "ready"
}
type IdentityStatus = { identity: SigningIdentity; hasHistory: boolean }

export default function LocalSigningIdentity() {
  const [status, setStatus] = useState<IdentityStatus | null>(null)
  const [passphrase, setPassphrase] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")

  async function refresh() {
    setError("")
    setStatus(await localRequest<IdentityStatus>("/admin/offline-key/public"))
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: load and refresh the owner service identity state.
  useEffect(() => {
    void refresh().catch((cause) => setError(cause.message))
  }, [])

  async function migrate(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError("")
    try {
      await localRequest("/admin/offline-key/service-enable", { passphrase })
      setPassphrase("")
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "操作未完成，请重试")
    } finally {
      setBusy(false)
    }
  }

  const identity = status?.identity
  const stateLabel = identity ? { uncreated: "未创建", locked: "待迁移", ready: "已就绪" }[identity.state] : "正在读取"

  return (
    <section className="grid gap-5 rounded-xl border bg-card p-6" data-testid="signing-identity">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">签发身份</h2>
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => void refresh().catch((cause) => setError(cause.message))}
        >
          刷新
        </Button>
      </div>
      <div role="status">{status?.hasHistory && identity?.state === "uncreated" ? "原签发身份缺失" : stateLabel}</div>
      {identity?.keyId && (
        <dl className="grid grid-cols-[7rem_1fr] gap-2 text-sm">
          <dt>身份编号</dt>
          <dd className="break-all">{identity.keyId}</dd>
        </dl>
      )}
      {identity?.state === "ready" && (
        <Button
          variant="outline"
          className="justify-self-start"
          disabled={busy}
          onClick={() => {
            const body = JSON.stringify(
              {
                formatVersion: 1,
                productId: "cn.snailfinance.desktop",
                purpose: "commercial",
                authorizationMode: "required",
                licenseKey: { keyId: identity.keyId, publicKeyPem: identity.publicKeyPem },
              },
              null,
              2,
            )
            downloadBlob(new Blob([body], { type: "application/json" }), "小团雀商业授权公钥.json")
          }}
        >
          导出小团雀公钥
        </Button>
      )}
      {identity?.state === "locked" && (
        <form className="grid max-w-lg gap-4" onSubmit={migrate}>
          <div className="grid gap-2">
            <Label htmlFor="legacy-signing-passphrase">原签发密码</Label>
            <Input
              id="legacy-signing-passphrase"
              type="password"
              autoComplete="off"
              value={passphrase}
              onChange={(event) => setPassphrase(event.target.value)}
              disabled={busy}
              required
            />
          </div>
          <Button className="justify-self-start" disabled={busy}>
            {busy ? "正在迁移…" : "启用自动签发"}
          </Button>
        </form>
      )}
      {identity?.state === "uncreated" && !status?.hasHistory && (
        <div className="text-sm text-muted-foreground">服务会自动创建并保护签发身份。</div>
      )}
      {identity?.state === "uncreated" && status?.hasHistory && (
        <div className="text-sm text-muted-foreground">请从包含原签发身份的备份恢复。</div>
      )}
      {error && (
        <div role="alert" className="text-sm text-destructive">
          {error}
        </div>
      )}
    </section>
  )
}
