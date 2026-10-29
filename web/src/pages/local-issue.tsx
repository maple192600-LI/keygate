import { type FormEvent, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { LocalAPIError, localRequest } from "@/lib/local-offline-api"
import type { CommercialCustomer } from "./local-customers"

type Term = {
  kind: "days" | "months" | "years" | "date" | "perpetual"
  amount?: number
  endDate?: string
  startsAt?: string
}
type Intent = {
  action: "initial" | "renew" | "transfer"
  customerId?: string
  licenseId?: string
  expectedRevision?: number
  deviceLimit?: number
  reason?: string
  term?: Term
}
type Preview = {
  previewBase: string
  activationAnchor?: "first-successful-activation" | "scheduled"
  customerName?: string
  displayNumber?: string
  revision?: number
  deviceLimit?: number
  notBefore: string | null
  expiresAt: string | null
}
type Issued = {
  licenseId: string
  licenseNumber?: string
  licenseCode?: string
  displayNumber: string
  revision: number
  deviceLimit: number
  activationAnchor?: "first-successful-activation" | "scheduled"
  notBefore: string | null
  expiresAt: string | null
}
type Command = Intent & { operationId: string; previewBase: string }
const dateText = (value: string | null) =>
  value === null
    ? "永久"
    : new Intl.DateTimeFormat("zh-CN", {
        timeZone: "Asia/Shanghai",
        dateStyle: "medium",
        timeStyle: "medium",
        hour12: false,
      }).format(new Date(value))
const durationText = (kind: Term["kind"], amount: string) =>
  kind === "perpetual"
    ? "永久"
    : kind === "date"
      ? `有效至 ${amount}`
      : `首次成功激活后 ${amount}${{ days: "天", months: "个月", years: "年" }[kind]}`
const licenseNumber = (id: string) => `L-${id.replaceAll("-", "").toUpperCase()}`
const failed = (cause: unknown) => (cause instanceof Error ? cause.message : "操作失败，请重试")

export default function LocalIssue({
  customer,
  onClose,
  existing,
}: {
  customer: CommercialCustomer
  onClose: () => void
  existing?: { id: string; revision: number; action: "renew" | "transfer"; bindingId?: string }
}) {
  const action = existing?.action || "initial"
  const title = action === "renew" ? "续费授权" : action === "transfer" ? "办理换机" : "发放授权"
  const [kind, setKind] = useState<Term["kind"]>("days")
  const [amount, setAmount] = useState("365")
  const [endDate, setEndDate] = useState("")
  const [starts, setStarts] = useState("")
  const [deviceLimit, setDeviceLimit] = useState("1")
  const [reason, setReason] = useState("")
  const [prepared, setPrepared] = useState<{ intent: Intent; preview: Preview } | null>(null)
  const [result, setResult] = useState<Issued | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [uncertain, setUncertain] = useState(false)
  const attempt = useRef<Command | null>(null)

  async function perform(run: () => Promise<void>) {
    if (busy) return
    setBusy(true)
    setError("")
    setNotice("")
    try {
      await run()
    } catch (cause) {
      setError(failed(cause))
    } finally {
      setBusy(false)
    }
  }

  async function preview(event: FormEvent) {
    event.preventDefault()
    await perform(async () => {
      const term: Term | undefined = action === "transfer" ? undefined : { kind }
      if (term && (kind === "days" || kind === "months" || kind === "years")) {
        const parsed = Number(amount)
        if (!Number.isSafeInteger(parsed) || parsed < 1) throw new Error("期限数量须为正整数")
        term.amount = parsed
      }
      if (kind === "date" && term) {
        if (!endDate) throw new Error("请选择授权截止日期")
        term.endDate = endDate
      }
      if (starts && term && action === "initial") term.startsAt = starts
      const count = Number(deviceLimit)
      if (action === "initial" && (!Number.isSafeInteger(count) || count < 1)) throw new Error("设备数量须为正整数")
      if (action === "transfer" && !reason.trim()) throw new Error("请填写换机原因")

      const intent: Intent = {
        action,
        ...(action === "initial" ? { customerId: customer.id, deviceLimit: count } : {}),
        ...(term ? { term } : {}),
        ...(existing ? { licenseId: existing.id, expectedRevision: existing.revision } : {}),
        ...(action === "transfer"
          ? { reason: reason.trim(), ...(existing?.bindingId ? { bindingId: existing.bindingId } : {}) }
          : {}),
      }
      const value = await localRequest<Preview>("/admin/offline-licenses/preview", intent)
      attempt.current = null
      setUncertain(false)
      setPrepared({ intent, preview: value })
    })
  }

  async function issue() {
    if (!prepared) return
    await perform(async () => {
      attempt.current ??= {
        ...prepared.intent,
        previewBase: prepared.preview.previewBase,
        operationId: crypto.randomUUID(),
      }
      const path =
        action === "initial" ? "/admin/offline-licenses" : `/admin/offline-licenses/${existing!.id}/${action}`
      try {
        const issued = await localRequest<Issued>(path, attempt.current)
        setResult({
          ...issued,
          licenseNumber: licenseNumber(issued.licenseId),
          activationAnchor: issued.activationAnchor ?? prepared.preview.activationAnchor,
        })
        setUncertain(false)
      } catch (cause) {
        setUncertain(
          cause instanceof LocalAPIError && (cause.code === "ISSUANCE_RESULT_UNKNOWN" || cause.status >= 500),
        )
        throw cause
      }
    })
  }

  async function recover() {
    if (!attempt.current) return
    await perform(async () => {
      try {
        const value = await localRequest<Issued>(`/admin/issuance-operations/${attempt.current!.operationId}`)
        const licenseCode =
          value.licenseCode ??
          (await localRequest<{ licenseCode: string }>(`/admin/offline-licenses/${value.licenseId}/code`)).licenseCode
        setResult({
          ...value,
          licenseNumber: licenseNumber(value.licenseId),
          licenseCode,
          activationAnchor: value.activationAnchor ?? prepared?.preview.activationAnchor,
        })
        setUncertain(false)
      } catch (cause) {
        if (cause instanceof LocalAPIError && cause.status === 404) {
          setUncertain(false)
          setNotice("未查到提交结果，可按原操作重试")
          return
        }
        throw cause
      }
    })
  }

  async function copyCode() {
    if (!result?.licenseCode) return
    await perform(async () => {
      await navigator.clipboard.writeText(result.licenseCode!)
      setNotice("授权码已复制")
    })
  }

  const effectivePeriod =
    prepared?.preview.activationAnchor === "first-successful-activation" && prepared.intent.term
      ? durationText(
          prepared.intent.term.kind,
          prepared.intent.term.kind === "date"
            ? prepared.intent.term.endDate || ""
            : String(prepared.intent.term.amount || ""),
        )
      : dateText(prepared?.preview.expiresAt ?? null)

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy && !uncertain) onClose()
      }}
    >
      <DialogContent className="lc-dialog max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {result ? "授权已办理" : title} · {customer.displayName}
          </DialogTitle>
        </DialogHeader>
        <DialogBody>
          {error && (
            <div role="alert" className="text-sm text-destructive">
              {error}
            </div>
          )}
          {notice && (
            <div role="status" className="text-sm">
              {notice}
            </div>
          )}
          {result ? (
            <div className="grid gap-5">
              <dl className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
                <dt>授权编号</dt>
                <dd>{licenseNumber(result.licenseId)}</dd>
                {result.licenseCode && (
                  <>
                    <dt>授权码</dt>
                    <dd className="break-all">{result.licenseCode}</dd>
                  </>
                )}
                <dt>生效时间</dt>
                <dd>{result.notBefore ? dateText(result.notBefore) : "首次成功激活时起算"}</dd>
                <dt>有效期限</dt>
                <dd>
                  {result.activationAnchor === "first-successful-activation"
                    ? durationText(kind, amount)
                    : dateText(result.expiresAt)}
                </dd>
                <dt>设备数量</dt>
                <dd>{result.deviceLimit} 台</dd>
                <dt>授权版本</dt>
                <dd>{result.revision}</dd>
              </dl>
              {result.licenseCode && (
                <Button disabled={busy} onClick={() => void copyCode()}>
                  复制授权码
                </Button>
              )}
              {action === "initial" && !result.notBefore && (
                <p>首次成功激活后开始计算期限。请将授权码交给客户妥善保存。</p>
              )}
            </div>
          ) : prepared ? (
            <div className="grid gap-5">
              <dl className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
                <dt>客户</dt>
                <dd>{prepared.preview.customerName || customer.displayName}</dd>
                <dt>授权编号</dt>
                <dd>{existing ? licenseNumber(existing.id) : "签发后生成"}</dd>
                {action === "initial" && (
                  <>
                    <dt>设备数量</dt>
                    <dd>{prepared.preview.deviceLimit || Number(deviceLimit)} 台</dd>
                  </>
                )}
                <dt>生效时间</dt>
                <dd>{prepared.preview.notBefore ? dateText(prepared.preview.notBefore) : "首次成功激活时起算"}</dd>
                <dt>有效期限</dt>
                <dd>{effectivePeriod}</dd>
                {action !== "initial" && (
                  <>
                    <dt>处理原因</dt>
                    <dd>{reason}</dd>
                  </>
                )}
              </dl>
              <div className="flex gap-3">
                <Button disabled={busy} onClick={() => void issue()}>
                  {busy
                    ? "正在保存…"
                    : attempt.current
                      ? "按原操作重试"
                      : action === "transfer"
                        ? "确认换机"
                        : "确认办理"}
                </Button>
                <Button
                  variant="outline"
                  disabled={busy || uncertain}
                  onClick={() => {
                    setPrepared(null)
                    attempt.current = null
                  }}
                >
                  修改条件
                </Button>
                {attempt.current && (
                  <Button variant="outline" disabled={busy} onClick={() => void recover()}>
                    查询本次结果
                  </Button>
                )}
              </div>
            </div>
          ) : (
            <form className="grid grid-cols-2 gap-4" onSubmit={preview}>
              {action !== "transfer" && (
                <>
                  <div className="grid gap-2">
                    <Label htmlFor="license-term-kind">授权期限</Label>
                    <select
                      id="license-term-kind"
                      className="h-9 rounded-md border border-input px-3"
                      value={kind}
                      onChange={(event) => setKind(event.target.value as Term["kind"])}
                      disabled={busy}
                    >
                      <option value="days">按天</option>
                      <option value="months">按月</option>
                      <option value="years">按年</option>
                      <option value="date">指定到期日</option>
                      <option value="perpetual">永久</option>
                    </select>
                  </div>
                  {(kind === "days" || kind === "months" || kind === "years") && (
                    <div className="grid gap-2">
                      <Label htmlFor="license-term-amount">数量</Label>
                      <Input
                        id="license-term-amount"
                        type="number"
                        min="1"
                        step="1"
                        value={amount}
                        onChange={(event) => setAmount(event.target.value)}
                        disabled={busy}
                        required
                      />
                    </div>
                  )}
                  {kind === "date" && (
                    <div className="grid gap-2">
                      <Label htmlFor="license-end-date">有效至</Label>
                      <Input
                        id="license-end-date"
                        type="date"
                        value={endDate}
                        onChange={(event) => setEndDate(event.target.value)}
                        disabled={busy}
                        required
                      />
                    </div>
                  )}
                  {action === "initial" && (
                    <div className="grid gap-2">
                      <Label htmlFor="license-device-limit">允许设备数</Label>
                      <Input
                        id="license-device-limit"
                        type="number"
                        min="1"
                        step="1"
                        value={deviceLimit}
                        onChange={(event) => setDeviceLimit(event.target.value)}
                        disabled={busy}
                        required
                      />
                    </div>
                  )}
                  {action === "initial" && (
                    <div className="col-span-2 grid gap-2">
                      <Label htmlFor="license-starts-at">指定生效日期</Label>
                      <Input
                        id="license-starts-at"
                        type="date"
                        value={starts}
                        onChange={(event) => setStarts(event.target.value)}
                        disabled={busy}
                      />
                    </div>
                  )}
                </>
              )}
              {action === "transfer" && (
                <div className="col-span-2 grid gap-2">
                  <Label htmlFor="transfer-reason">换机原因</Label>
                  <Input
                    id="transfer-reason"
                    value={reason}
                    onChange={(event) => setReason(event.target.value)}
                    maxLength={500}
                    disabled={busy}
                    required
                  />
                </div>
              )}
              <div className="col-span-2">
                <Button type="submit" disabled={busy}>
                  {busy ? "正在检查…" : action === "transfer" ? "预览换机" : "预览授权条件"}
                </Button>
              </div>
            </form>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
