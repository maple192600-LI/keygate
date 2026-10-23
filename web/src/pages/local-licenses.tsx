import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { localDownload, localRequest } from "@/lib/local-offline-api"
import { DeliveryForm, type Issued } from "./local-issue"
import IssuanceStateForm from "./local-state-form"
export type OfflineLicense = {
  id: string
  displayNumber: string
  customerId: string
  customerName: string
  deviceLabel: string
  deviceDigest: string
  notBefore: string
  expiresAt: string | null
  revision: number
  issuanceId: string
  keyId: string
  sha256: string
  fileName: string
  termState: "not_yet_valid" | "valid" | "expired" | "perpetual"
  deliveryState: "pending" | "recorded"
  issuanceEnabled: boolean
  customerIssuanceEnabled: boolean
  usageState: "unreported"
  deliveryMethod: string
  deliveryRecipient: string
  deliveredAt: string | null
}
type Issuance = {
  id: string
  revision: number
  kind: string
  fileName: string
  sha256: string
  notBefore: string
  expiresAt: string | null
  issuedAt: string
}
type Binding = {
  id: string
  deviceLabel: string
  deviceDigest: string
  registeredAt: string
  supersededAt: string | null
}
type Delivery = {
  id: string
  issuanceId: string
  method: string
  recipient: string
  deliveredAt: string
  notes: string
}
const terms = { not_yet_valid: "未生效", valid: "有效", expired: "已到期", perpetual: "永久" }
const methods: Record<string, string> = { wechat: "微信", email: "邮件", usb: "U盘", other: "其他" }
const dateText = (value: string | null) =>
  value === null
    ? "永久"
    : new Intl.DateTimeFormat("zh-CN", {
        timeZone: "Asia/Shanghai",
        dateStyle: "medium",
        timeStyle: "short",
        hour12: false,
      }).format(new Date(value))
const issued = (row: OfflineLicense): Issued => ({
  licenseId: row.id,
  issuanceId: row.issuanceId,
  revision: row.revision,
  sha256: row.sha256,
  fileName: row.fileName,
  keyId: row.keyId,
  notBefore: row.notBefore,
  expiresAt: row.expiresAt,
  replayed: true,
})
export type LicenseAction = (row: OfflineLicense, action: "renew" | "transfer") => void

export default function LocalLicenses({
  delivery = false,
  customerId,
  compact = false,
  initialExpiring = false,
  initialTermState = "",
  initialDeliveryState = "",
  title,
  epoch = 0,
  onCreate,
  onManage,
}: {
  delivery?: boolean
  customerId?: string
  compact?: boolean
  initialExpiring?: boolean
  initialTermState?: string
  initialDeliveryState?: string
  title?: string
  epoch?: number
  onCreate?: () => void
  onManage?: LicenseAction
}) {
  const [items, setItems] = useState<OfflineLicense[]>([]),
    [total, setTotal] = useState(0),
    [query, setQuery] = useState(""),
    [submitted, setSubmitted] = useState("")
  const [term, setTerm] = useState(initialTermState),
    [deliveryState, setDelivery] = useState(initialDeliveryState),
    [page, setPage] = useState(1),
    [reload, setReload] = useState(0),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [selected, setSelected] = useState<string | null>(null)
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload and epoch intentionally refresh the current license query.
  useEffect(() => {
    let alive = true
    setBusy(true)
    const params = new URLSearchParams({
      q: submitted,
      termState: term,
      deliveryState,
      page: String(page),
      pageSize: compact ? "5" : "20",
      ...(customerId ? { customerId } : {}),
      ...(initialExpiring ? { expiring: "30" } : {}),
    })
    localRequest<{ items: OfflineLicense[]; total: number }>(`/admin/offline-licenses?${params}`)
      .then((value) => {
        if (alive) {
          setItems(value.items)
          setTotal(value.total)
          setError("")
        }
      })
      .catch((cause) => {
        if (alive) setError(cause.message)
      })
      .finally(() => {
        if (alive) setBusy(false)
      })
    return () => {
      alive = false
    }
  }, [submitted, term, deliveryState, page, reload, customerId, initialExpiring, compact, epoch])
  return (
    <section className={compact ? "lc-list-page lc-compact-list" : "lc-list-page"}>
      {!compact && (
        <div className="lc-page-heading">
          <h1>{title || (delivery ? "交付记录" : "授权")}</h1>
          {onCreate && <Button onClick={onCreate}>发放授权</Button>}
        </div>
      )}
      {error && (
        <div role="alert" className="text-destructive">
          {error}
        </div>
      )}
      <div className="lc-list-panel">
        <div className="lc-toolbar">
          {compact && <h2>{title}</h2>}
          {!compact && (
            <form
              className="flex items-center gap-2"
              onSubmit={(event) => {
                event.preventDefault()
                setSubmitted(query)
                setPage(1)
              }}
            >
              <Input
                className="w-72"
                aria-label="查询授权"
                placeholder="客户、授权编号、设备或来源"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <Button variant="outline" type="submit">
                查询
              </Button>
            </form>
          )}
          <Button variant="ghost" disabled={busy} onClick={() => setReload((value) => value + 1)}>
            刷新
          </Button>
          {!compact && (
            <div className="flex flex-wrap gap-2">
              {!delivery && (
                <select
                  aria-label="期限状态"
                  className="h-9 rounded-md border border-input px-3"
                  value={term}
                  onChange={(event) => {
                    setTerm(event.target.value)
                    setPage(1)
                  }}
                >
                  <option value="">全部期限</option>
                  <option value="active">有效（含永久）</option>
                  {Object.entries(terms).map(([key, label]) => (
                    <option key={key} value={key}>
                      {label}
                    </option>
                  ))}
                </select>
              )}
              <select
                aria-label="交付状态"
                className="h-9 rounded-md border border-input px-3"
                value={deliveryState}
                onChange={(event) => {
                  setDelivery(event.target.value)
                  setPage(1)
                }}
              >
                <option value="">全部交付</option>
                <option value="pending">待交付</option>
                <option value="recorded">已登记交付</option>
              </select>
              {initialExpiring && <span className="self-center text-sm">30天内到期</span>}
            </div>
          )}
        </div>
        <div className="overflow-x-auto">
          <Table className={compact ? "lc-compact-table" : delivery ? "lc-delivery-table" : "lc-license-table"}>
            <TableHeader>
              <TableRow>
                <TableHead>客户／授权编号</TableHead>
                {!compact && !delivery && <TableHead>绑定设备</TableHead>}
                {!delivery && (
                  <>
                    <TableHead>期限状态</TableHead>
                    <TableHead>截止时间</TableHead>
                  </>
                )}
                <TableHead>交付状态</TableHead>
                {delivery && (
                  <>
                    <TableHead>交付方式</TableHead>
                    <TableHead>收件对象</TableHead>
                    <TableHead>交付时间</TableHead>
                  </>
                )}
                {!compact && !delivery && <TableHead>续发</TableHead>}
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((row) => (
                <TableRow key={row.id}>
                  <TableCell>
                    <span className="lc-license-customer">{row.customerName}</span>
                    <span className="lc-record-number">{row.displayNumber}</span>
                  </TableCell>
                  {!compact && !delivery && <TableCell>{row.deviceLabel || "申请设备"}</TableCell>}
                  {!delivery && (
                    <>
                      <TableCell>
                        <span className={`lc-status lc-status-${row.termState}`}>{terms[row.termState]}</span>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">{dateText(row.expiresAt)}</TableCell>
                    </>
                  )}
                  <TableCell>
                    <span className={`lc-status lc-status-${row.deliveryState}`}>
                      {row.deliveryState === "recorded" ? "已登记交付" : "待交付"}
                    </span>
                  </TableCell>
                  {delivery && (
                    <>
                      <TableCell>{methods[row.deliveryMethod] || "—"}</TableCell>
                      <TableCell>{row.deliveryRecipient || "—"}</TableCell>
                      <TableCell>{row.deliveredAt ? dateText(row.deliveredAt) : "—"}</TableCell>
                    </>
                  )}
                  {!compact && !delivery && (
                    <TableCell>{row.issuanceEnabled && row.customerIssuanceEnabled ? "可续发" : "已停发"}</TableCell>
                  )}
                  <TableCell>
                    <div className="lc-row-actions">
                      {delivery && (
                        <Button
                          variant="link"
                          size="sm"
                          onClick={async () => {
                            try {
                              await localDownload(`/admin/license-issuances/${row.issuanceId}/file`, row.fileName)
                            } catch (cause) {
                              setError((cause as Error).message)
                            }
                          }}
                        >
                          下载原件
                        </Button>
                      )}
                      <Button variant="link" size="sm" onClick={() => setSelected(row.id)}>
                        {delivery ? (row.deliveryState === "pending" ? "登记交付" : "查看记录") : "查看详情"}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {!items.length && (
                <TableRow>
                  <TableCell colSpan={compact ? 5 : delivery ? 6 : 7}>
                    <div className="lc-empty" role="status">
                      {busy ? "正在读取授权…" : error ? "授权未能读取" : delivery ? "暂无交付记录" : "暂无授权"}
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
        {!compact && (
          <div className="lc-pagination">
            <span>
              共 {total} 份{delivery ? "原件" : "授权"}
            </span>
            {total > 20 && (
              <div>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy || page === 1}
                  onClick={() => setPage((value) => value - 1)}
                >
                  上一页
                </Button>
                <span>{page}</span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy || page * 20 >= total}
                  onClick={() => setPage((value) => value + 1)}
                >
                  下一页
                </Button>
              </div>
            )}
          </div>
        )}
      </div>
      {selected && (
        <LicenseDetails
          id={selected}
          onClose={() => {
            setSelected(null)
            setReload((value) => value + 1)
          }}
          onManage={
            onManage
              ? (row, action) => {
                  setSelected(null)
                  onManage(row, action)
                }
              : undefined
          }
        />
      )}
    </section>
  )
}
function LicenseDetails({ id, onClose, onManage }: { id: string; onClose: () => void; onManage?: LicenseAction }) {
  const [data, setData] = useState<{
      license: OfflineLicense
      issuances: Issuance[]
      bindings: Binding[]
      deliveries: Delivery[]
    } | null>(null),
    [error, setError] = useState(""),
    [reload, setReload] = useState(0)
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload refreshes details after a lifecycle or delivery operation.
  useEffect(() => {
    let alive = true
    localRequest<typeof data>(`/admin/offline-licenses/${id}`)
      .then((value) => {
        if (alive) {
          setData(value)
          setError("")
        }
      })
      .catch((cause) => {
        if (alive) setError(cause.message)
      })
    return () => {
      alive = false
    }
  }, [id, reload])
  async function download(issuance: Issuance | Issued) {
    try {
      await localDownload(
        `/admin/license-issuances/${"issuanceId" in issuance ? issuance.issuanceId : issuance.id}/file`,
        issuance.fileName,
      )
    } catch (cause) {
      setError((cause as Error).message)
    }
  }
  const row = data?.license
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <SheetContent side="right" label="授权详情" className="lc-drawer w-[840px] max-w-[90vw] overflow-y-auto p-7">
        <h2 className="text-xl font-semibold">授权详情</h2>
        <div className="lc-drawer-body">
          {error && (
            <div role="alert" className="text-destructive">
              {error}
            </div>
          )}
          {row && (
            <div className="grid gap-6">
              <dl className="grid grid-cols-[7rem_1fr] gap-3 text-sm">
                <dt>客户</dt>
                <dd>{row.customerName}</dd>
                <dt>授权编号</dt>
                <dd>{row.displayNumber}</dd>
                <dt>期限状态</dt>
                <dd>{terms[row.termState]}</dd>
                <dt>生效时间</dt>
                <dd>{dateText(row.notBefore)}</dd>
                <dt>截止时间</dt>
                <dd>{dateText(row.expiresAt)}</dd>
                <dt>当前版本</dt>
                <dd>{row.revision}</dd>
                <dt>交付状态</dt>
                <dd>{row.deliveryState === "recorded" ? "已登记交付" : "待交付"}</dd>
                <dt>离线使用</dt>
                <dd>未回传</dd>
                <dt>设备码</dt>
                <dd className="break-all">STQ-WIN1-{row.deviceDigest}</dd>
              </dl>
              <div className="flex gap-3">
                <Button onClick={() => void download(issued(row))}>下载当前原件</Button>
                {onManage && (
                  <>
                    <Button
                      variant="outline"
                      disabled={row.expiresAt === null || !row.issuanceEnabled || !row.customerIssuanceEnabled}
                      onClick={() => onManage(row, "renew")}
                    >
                      续期
                    </Button>
                    <Button
                      variant="outline"
                      disabled={row.termState === "expired" || !row.issuanceEnabled || !row.customerIssuanceEnabled}
                      onClick={() => onManage(row, "transfer")}
                    >
                      换机
                    </Button>
                  </>
                )}
              </div>
              <IssuanceStateForm
                kind="license"
                id={row.id}
                enabled={row.issuanceEnabled}
                revision={row.revision}
                onChanged={() => setReload((value) => value + 1)}
              />
              {row.deliveryState === "pending" && (
                <DeliveryForm issuance={issued(row)} onRecorded={() => setReload((value) => value + 1)} />
              )}
              <section className="grid gap-3">
                <h3 className="font-semibold">历次签发</h3>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>版本</TableHead>
                      <TableHead>动作</TableHead>
                      <TableHead>签发时间</TableHead>
                      <TableHead>原件</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.issuances.map((value) => (
                      <TableRow key={value.id}>
                        <TableCell>{value.revision}</TableCell>
                        <TableCell>
                          {{ initial: "首次签发", renew: "续期", transfer: "换机" }[value.kind] || value.kind}
                        </TableCell>
                        <TableCell>{dateText(value.issuedAt)}</TableCell>
                        <TableCell>
                          <Button variant="outline" size="sm" onClick={() => void download(value)}>
                            下载原件
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </section>
              <section className="grid gap-3">
                <h3 className="font-semibold">交付记录</h3>
                {data.deliveries.length ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>方式</TableHead>
                        <TableHead>收件对象</TableHead>
                        <TableHead>时间</TableHead>
                        <TableHead>备注</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {data.deliveries.map((value) => (
                        <TableRow key={value.id}>
                          <TableCell>{methods[value.method]}</TableCell>
                          <TableCell>{value.recipient}</TableCell>
                          <TableCell>{dateText(value.deliveredAt)}</TableCell>
                          <TableCell>{value.notes || "—"}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <span className="text-sm text-muted-foreground">暂无交付记录</span>
                )}
              </section>
              <section className="grid gap-3">
                <h3 className="font-semibold">设备记录</h3>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>设备名称</TableHead>
                      <TableHead>登记时间</TableHead>
                      <TableHead>状态</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.bindings.map((value) => (
                      <TableRow key={value.id}>
                        <TableCell>{value.deviceLabel || "申请设备"}</TableCell>
                        <TableCell>{dateText(value.registeredAt)}</TableCell>
                        <TableCell>{value.supersededAt ? "已替换绑定" : "当前登记"}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </section>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
