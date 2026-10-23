import { type FormEvent, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { LocalAPIError, localDownload, localRequest, readLocalFile } from "@/lib/local-offline-api"
import type { CommercialCustomer } from "./local-customers"

type Term = { kind: "days" | "months" | "date" | "perpetual"; amount?: number; endDate?: string; startsAt?: string }
type Intent = {
  action: "initial" | "renew" | "transfer"
  customerId?: string
  licenseId?: string
  requestId?: string
  deviceLabel?: string
  expectedRevision?: number
  reason?: string
  term?: Term
}
type Preview = {
  baseTime: string
  previewBase: string
  customerName: string
  deviceLabel: string
  deviceCode: string
  notBefore: string
  expiresAt: string | null
}
export type Issued = {
  licenseId: string
  issuanceId: string
  revision: number
  sha256: string
  fileName: string
  keyId: string
  notBefore: string
  expiresAt: string | null
  replayed: boolean
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
const failed = (cause: unknown) => (cause instanceof Error ? cause.message : "操作失败，请重试")

export default function LocalIssue({
  customer,
  onClose,
  onSettings,
  existing,
}: {
  customer: CommercialCustomer
  onClose: () => void
  onSettings: () => void
  existing?: { id: string; revision: number; action: "renew" | "transfer" }
}) {
  const [kind, setKind] = useState<Term["kind"]>("days"),
    [amount, setAmount] = useState("365"),
    [endDate, setDate] = useState(""),
    [starts, setStarts] = useState("")
  const [requestFile, setRequestFile] = useState(""),
    [deviceCode, setDeviceCode] = useState(""),
    [label, setLabel] = useState(""),
    [reason, setReason] = useState("")
  const [prepared, setPrepared] = useState<{ intent: Intent; preview: Preview } | null>(null),
    [result, setResult] = useState<Issued | null>(null)
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [uncertain, setUncertain] = useState(false),
    [unlock, setUnlock] = useState("")
  const [delivered, setDelivered] = useState(false)
  const attempt = useRef<Command | null>(null),
    generation = useRef(0)
  const action = existing?.action || "initial",
    needsRequest = action !== "renew"
  const title = action === "renew" ? "续期授权" : action === "transfer" ? "换机授权" : "发放授权"
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
      let requestId: string | undefined
      if (needsRequest) {
        const imported = await localRequest<{ request: { requestId: string } }>(
          "/admin/offline-requests",
          requestFile ? { requestFile } : { deviceCode, deviceLabel: label },
        )
        requestId = imported.request.requestId
      }
      const term: Term | undefined = action === "transfer" ? undefined : { kind }
      if (term) {
        if (kind === "days" || kind === "months") {
          const parsed = Number(amount)
          if (!Number.isSafeInteger(parsed) || parsed < 1) throw new Error("期限数量须为正整数")
          term.amount = parsed
        }
        if (kind === "date") term.endDate = endDate
        if (starts && !existing) term.startsAt = new Date(`${starts}:00+08:00`).toISOString()
      }
      const intent: Intent = {
        action,
        customerId: customer.id,
        ...(term ? { term } : {}),
        ...(requestId ? { requestId, deviceLabel: label } : {}),
        ...(existing ? { licenseId: existing.id, expectedRevision: existing.revision, reason } : {}),
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
        setResult(await localRequest<Issued>(path, attempt.current))
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
        setResult(value)
        setUncertain(false)
      } catch (cause) {
        if (cause instanceof LocalAPIError && cause.status === 404) {
          setUncertain(false)
          setNotice("未查到已提交结果，可按原操作重试")
          return
        }
        throw cause
      }
    })
  }
  const copyText = result
    ? `${customer.displayName}：蜗牛财务授权文件 ${result.fileName}，${result.expiresAt === null ? "永久有效" : `有效至北京时间 ${dateText(result.expiresAt)} 之前`}。请在蜗牛财务中选择“导入授权文件”，导入后按提示创建本机管理员。此文件仅适用于申请设备。`
    : ""
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
            {result ? "签发完成" : title} · {customer.displayName}
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
                <dt>生效时间</dt>
                <dd>{dateText(result.notBefore)}</dd>
                <dt>有效期限</dt>
                <dd>{dateText(result.expiresAt)}</dd>
                <dt>授权版本</dt>
                <dd>{result.revision}</dd>
                <dt>交付状态</dt>
                <dd data-testid="delivery-status">{delivered ? "已登记交付" : "待交付"}</dd>
              </dl>
              <div className="flex gap-3">
                <Button
                  disabled={busy}
                  onClick={() =>
                    void perform(() =>
                      localDownload(`/admin/license-issuances/${result.issuanceId}/file`, result.fileName),
                    )
                  }
                >
                  下载授权文件
                </Button>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() =>
                    void perform(async () => {
                      await navigator.clipboard.writeText(copyText)
                      setNotice("交付说明已复制")
                    })
                  }
                >
                  复制交付说明
                </Button>
              </div>
              <DeliveryForm issuance={result} onRecorded={() => setDelivered(true)} />
            </div>
          ) : prepared ? (
            <div className="grid gap-5">
              <dl className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
                <dt>客户</dt>
                <dd>{prepared.preview.customerName}</dd>
                {(customer.contactName || customer.contactMethod) && (
                  <>
                    <dt>联系资料</dt>
                    <dd>{[customer.contactName, customer.contactMethod].filter(Boolean).join(" · ")}</dd>
                  </>
                )}
                {customer.email && (
                  <>
                    <dt>邮箱</dt>
                    <dd>{customer.email}</dd>
                  </>
                )}
                {customer.sourceText && (
                  <>
                    <dt>来源／介绍人</dt>
                    <dd>{customer.sourceText}</dd>
                  </>
                )}
                {customer.notes && (
                  <>
                    <dt>客户备注</dt>
                    <dd>{customer.notes}</dd>
                  </>
                )}
                <dt>设备</dt>
                <dd>{prepared.preview.deviceLabel || "申请设备"}</dd>
                <dt>设备码</dt>
                <dd className="break-all">{prepared.preview.deviceCode}</dd>
                <dt>生效时间</dt>
                <dd>{dateText(prepared.preview.notBefore)}</dd>
                <dt>有效期限</dt>
                <dd>{dateText(prepared.preview.expiresAt)}</dd>
              </dl>
              <div className="flex gap-3">
                <Button disabled={busy} onClick={() => void issue()}>
                  {busy ? "正在签发…" : attempt.current ? "按原操作重试" : "确认签发"}
                </Button>
                <Button
                  variant="outline"
                  disabled={busy || uncertain}
                  onClick={() => {
                    setPrepared(null)
                    attempt.current = null
                    setError("")
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
              <form
                className="flex items-end gap-3"
                onSubmit={(event) => {
                  event.preventDefault()
                  void perform(async () => {
                    await localRequest("/admin/offline-key/unlock", { passphrase: unlock })
                    setUnlock("")
                    setNotice("签发已解锁")
                  })
                }}
              >
                <div className="grid gap-2">
                  <Label htmlFor="issue-unlock">签发密码</Label>
                  <Input
                    id="issue-unlock"
                    type="password"
                    autoComplete="off"
                    value={unlock}
                    onChange={(event) => setUnlock(event.target.value)}
                    disabled={busy || uncertain}
                  />
                </div>
                <Button variant="outline" type="submit" disabled={busy || !unlock || uncertain}>
                  解锁签发
                </Button>
                <Button variant="outline" type="button" disabled={busy || uncertain} onClick={onSettings}>
                  签发身份设置
                </Button>
              </form>
            </div>
          ) : (
            <form className="grid grid-cols-2 gap-4" onSubmit={preview}>
              {needsRequest && (
                <>
                  <div className="col-span-2 grid gap-2">
                    <Label htmlFor="request-file">授权申请文件</Label>
                    <Input
                      id="request-file"
                      type="file"
                      accept=".snailreq"
                      disabled={busy}
                      onChange={async (event) => {
                        const file = event.target.files?.[0],
                          current = ++generation.current
                        try {
                          const value = file ? await readLocalFile(file, 65536) : ""
                          if (current === generation.current) {
                            setRequestFile(value)
                            if (value) setDeviceCode("")
                          }
                        } catch (cause) {
                          setError(failed(cause))
                          setRequestFile("")
                        }
                      }}
                    />
                  </div>
                  <div className="col-span-2 grid gap-2">
                    <Label htmlFor="device-code">完整设备码</Label>
                    <Input
                      id="device-code"
                      placeholder="STQ-WIN1-"
                      value={deviceCode}
                      onChange={(event) => {
                        generation.current++
                        setDeviceCode(event.target.value)
                        setRequestFile("")
                      }}
                      disabled={busy || !!requestFile}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="device-label">设备名称</Label>
                    <Input
                      id="device-label"
                      maxLength={80}
                      value={label}
                      onChange={(event) => setLabel(event.target.value)}
                      disabled={busy}
                    />
                  </div>
                </>
              )}
              {action !== "transfer" && (
                <>
                  <div className="grid gap-2">
                    <Label htmlFor="term-kind">授权期限</Label>
                    <select
                      id="term-kind"
                      className="h-9 rounded-md border border-input px-3"
                      disabled={busy}
                      value={kind}
                      onChange={(event) => setKind(event.target.value as Term["kind"])}
                    >
                      <option value="days">按天</option>
                      <option value="months">按月</option>
                      <option value="date">指定截止日期</option>
                      <option value="perpetual">永久</option>
                    </select>
                  </div>
                  {(kind === "days" || kind === "months") && (
                    <div className="grid gap-2">
                      <Label htmlFor="term-amount">{kind === "days" ? "天数" : "月数"}</Label>
                      <Input
                        id="term-amount"
                        type="number"
                        min={1}
                        step={1}
                        value={amount}
                        onChange={(event) => setAmount(event.target.value)}
                        disabled={busy}
                        required
                      />
                    </div>
                  )}
                  {kind === "date" && (
                    <div className="grid gap-2">
                      <Label htmlFor="term-date">截止日期（含当日）</Label>
                      <Input
                        id="term-date"
                        type="date"
                        value={endDate}
                        onChange={(event) => setDate(event.target.value)}
                        disabled={busy}
                        required
                      />
                    </div>
                  )}
                  {!existing && (
                    <div className="grid gap-2">
                      <Label htmlFor="term-start">生效时间（北京时间）</Label>
                      <Input
                        id="term-start"
                        type="datetime-local"
                        value={starts}
                        onChange={(event) => setStarts(event.target.value)}
                        disabled={busy}
                      />
                    </div>
                  )}
                </>
              )}
              {existing && (
                <div className="col-span-2 grid gap-2">
                  <Label htmlFor="issue-reason">{action === "transfer" ? "换机原因" : "续期备注"}</Label>
                  <Input
                    id="issue-reason"
                    maxLength={1000}
                    value={reason}
                    onChange={(event) => setReason(event.target.value)}
                    required={action === "transfer"}
                    disabled={busy}
                  />
                </div>
              )}
              <div className="col-span-2 flex gap-3">
                <Button type="submit" disabled={busy}>
                  {busy ? "正在核对…" : "核对并预览"}
                </Button>
                <Button variant="outline" type="button" disabled={busy} onClick={onClose}>
                  取消
                </Button>
              </div>
            </form>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

export function DeliveryForm({ issuance, onRecorded }: { issuance: Issued; onRecorded: () => void }) {
  const [method, setMethod] = useState("wechat"),
    [recipient, setRecipient] = useState(""),
    [date, setDate] = useState(""),
    [notes, setNotes] = useState("")
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false),
    [uncertain, setUncertain] = useState(false)
  const attempt = useRef<{
    operationId: string
    method: string
    recipient: string
    deliveredAt: string
    notes: string
  } | null>(null)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError("")
    try {
      attempt.current ??= {
        operationId: crypto.randomUUID(),
        method,
        recipient,
        deliveredAt: new Date(`${date}:00+08:00`).toISOString(),
        notes,
      }
      await localRequest(`/admin/license-issuances/${issuance.issuanceId}/deliveries`, attempt.current)
      setSaved(true)
      setUncertain(false)
      onRecorded()
    } catch (cause) {
      setError(failed(cause))
      const unknown = cause instanceof LocalAPIError && cause.status >= 500
      setUncertain(unknown)
      if (!unknown) attempt.current = null
    } finally {
      setBusy(false)
    }
  }
  if (saved) return <div role="status">已登记交付</div>
  return (
    <form className="grid grid-cols-2 gap-4 border-t pt-5" onSubmit={submit}>
      <h3 className="col-span-2 font-medium">登记交付</h3>
      <div className="grid gap-2">
        <Label htmlFor="delivery-method">交付方式</Label>
        <select
          id="delivery-method"
          className="h-9 rounded-md border border-input px-3"
          value={method}
          onChange={(event) => setMethod(event.target.value)}
          disabled={busy || uncertain}
        >
          <option value="wechat">微信</option>
          <option value="email">邮件</option>
          <option value="usb">U盘</option>
          <option value="other">其他</option>
        </select>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="delivery-recipient">收件对象</Label>
        <Input
          id="delivery-recipient"
          value={recipient}
          onChange={(event) => setRecipient(event.target.value)}
          maxLength={200}
          disabled={busy || uncertain}
          required
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="delivery-date">实际交付时间（北京时间）</Label>
        <Input
          id="delivery-date"
          type="datetime-local"
          value={date}
          onChange={(event) => setDate(event.target.value)}
          disabled={busy || uncertain}
          required
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="delivery-notes">备注</Label>
        <Input
          id="delivery-notes"
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
          disabled={busy || uncertain}
        />
      </div>
      {error && (
        <div role="alert" className="col-span-2 text-destructive">
          {error}
        </div>
      )}
      <div className="col-span-2">
        <Button variant="outline" type="submit" disabled={busy}>
          {busy ? "正在保存…" : uncertain ? "按原操作重试登记" : "确认登记交付"}
        </Button>
      </div>
    </form>
  )
}
