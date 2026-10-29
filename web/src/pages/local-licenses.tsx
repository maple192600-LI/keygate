import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { localRequest } from "@/lib/local-offline-api"
import IssuanceStateForm from "./local-state-form"
export type OfflineLicense = {
  id: string
  customerNumber: string
  licenseNumber: string
  displayNumber: string
  customerId: string
  customerName: string
  contactMethod: string
  devices: Device[]
  deviceLimit: number
  term?: {
    kind: "days" | "months" | "years" | "date" | "perpetual"
    amount?: number
    endDate?: string
    startsAt?: string
  }
  notBefore: string | null
  expiresAt: string | null
  revision: number
  termState: "unactivated" | "not_yet_valid" | "valid" | "expired" | "perpetual"
  issuanceEnabled: boolean
  customerIssuanceEnabled: boolean
}
type Issuance = {
  id: string
  revision: number
  kind: string
  notBefore: string
  expiresAt: string | null
  issuedAt: string
}
type Device = {
  bindingId: string
  deviceLabel: string
  deviceState: "current" | "transferred"
  appVersion: string
  buildId: string
  lastReportedAt: string | null
}
const terms = { unactivated: "未激活", not_yet_valid: "待生效", valid: "使用中", expired: "已到期", perpetual: "永久" }
const licenseNumber = (id: string) => `L-${id.replaceAll("-", "").toUpperCase()}`
const termText = (term: NonNullable<OfflineLicense["term"]>) => {
  if (term.kind === "perpetual") return "永久"
  if (term.kind === "date") return `有效至 ${term.endDate}`
  return `${term.amount}${{ days: "天", months: "个月", years: "年" }[term.kind]}`
}
const dateText = (value: string | null) =>
  value === null
    ? "永久"
    : new Intl.DateTimeFormat("zh-CN", {
        timeZone: "Asia/Shanghai",
        dateStyle: "medium",
        timeStyle: "short",
        hour12: false,
      }).format(new Date(value))
export type LicenseAction = (row: OfflineLicense, action: "renew" | "transfer", bindingId?: string) => void

export default function LocalLicenses({
  customerId,
  compact = false,
  initialExpiring = false,
  initialTermState = "",
  title,
  epoch = 0,
  onCreate,
  onManage,
  onChanged,
}: {
  customerId?: string
  compact?: boolean
  initialExpiring?: boolean
  initialTermState?: string
  title?: string
  epoch?: number
  onCreate?: () => void
  onManage?: LicenseAction
  onChanged?: () => void
}) {
  const [items, setItems] = useState<OfflineLicense[]>([]),
    [total, setTotal] = useState(0),
    [query, setQuery] = useState(""),
    [submitted, setSubmitted] = useState("")
  const [term, setTerm] = useState(initialTermState),
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
      page: String(page),
      pageSize: compact ? "5" : "20",
      ...(customerId ? { customerId } : {}),
      ...(initialExpiring ? { expiring: "15d" } : {}),
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
  }, [submitted, term, page, reload, customerId, initialExpiring, compact, epoch])
  return (
    <section className={compact ? "lc-list-page lc-compact-list" : "lc-list-page"}>
      {!compact && (
        <div className="lc-page-heading">
          <h1>{title || "授权"}</h1>
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
                placeholder="客户、客户编号、授权编号、联系方式或设备"
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
              {initialExpiring && <span className="self-center text-sm">未来15天内</span>}
            </div>
          )}
        </div>
        <div className="overflow-x-auto">
          <Table className={compact ? "lc-compact-table" : "lc-license-table"}>
            <TableHeader>
              <TableRow>
                <TableHead>客户／授权编号</TableHead>
                {!compact && <TableHead>绑定设备</TableHead>}
                <TableHead>期限状态</TableHead>
                <TableHead>截止时间</TableHead>
                <TableHead>设备状态</TableHead>
                {!compact && <TableHead>续发</TableHead>}
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((row) => (
                <TableRow key={row.id}>
                  <TableCell>
                    <span className="lc-license-customer">{row.customerName}</span>
                    <span className="lc-record-number">{row.customerNumber}</span>
                    <span className="lc-record-number">{licenseNumber(row.id)}</span>
                  </TableCell>
                  {!compact && (
                    <TableCell>
                      {row.devices
                        ?.filter((device) => device.deviceState === "current")
                        .map((device) => device.deviceLabel || "已登记设备")
                        .join("、") || "未激活"}
                    </TableCell>
                  )}
                  <TableCell>
                    <span className={`lc-status lc-status-${row.termState}`}>{terms[row.termState]}</span>
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    {row.termState === "unactivated" ? "首次激活后起算" : dateText(row.expiresAt)}
                  </TableCell>
                  <TableCell>
                    {row.devices?.some((device) => device.deviceState === "current") ? "已绑定" : "未激活"}
                  </TableCell>
                  {!compact && (
                    <TableCell>{row.issuanceEnabled && row.customerIssuanceEnabled ? "可续发" : "已停发"}</TableCell>
                  )}
                  <TableCell>
                    <div className="lc-row-actions">
                      <Button variant="link" size="sm" onClick={() => setSelected(row.id)}>
                        查看详情
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {!items.length && (
                <TableRow>
                  <TableCell colSpan={compact ? 5 : 7}>
                    <div className="lc-empty" role="status">
                      {busy ? "正在读取授权…" : error ? "授权未能读取" : "暂无授权"}
                    </div>
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
        {!compact && (
          <div className="lc-pagination">
            <span>共 {total} 份授权</span>
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
          onChanged={() => {
            setReload((value) => value + 1)
            onChanged?.()
          }}
          onClose={() => {
            setSelected(null)
            setReload((value) => value + 1)
          }}
          onManage={
            onManage
              ? (row, action, bindingId) => {
                  setSelected(null)
                  onManage(row, action, bindingId)
                }
              : undefined
          }
        />
      )}
    </section>
  )
}
function LicenseDetails({
  id,
  onClose,
  onManage,
  onChanged,
}: {
  id: string
  onClose: () => void
  onManage?: LicenseAction
  onChanged?: () => void
}) {
  const [data, setData] = useState<{
      license: OfflineLicense
      issuances: Issuance[]
    } | null>(null),
    [error, setError] = useState(""),
    [reload, setReload] = useState(0),
    [licenseCode, setLicenseCode] = useState<string | null>(null),
    [codeVisible, setCodeVisible] = useState(false),
    [codeBusy, setCodeBusy] = useState(false)
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
  async function toggleCode() {
    if (codeVisible) {
      setCodeVisible(false)
      return
    }
    setCodeBusy(true)
    try {
      const value = await localRequest<{ licenseCode: string }>(`/admin/offline-licenses/${id}/code`)
      setLicenseCode(value.licenseCode)
      setCodeVisible(true)
    } catch (cause) {
      setError((cause as Error).message)
    } finally {
      setCodeBusy(false)
    }
  }
  async function copyCode() {
    if (!licenseCode) return
    try {
      await navigator.clipboard.writeText(licenseCode)
      setError("")
    } catch (cause) {
      setError((cause as Error).message)
    }
  }
  const row = data?.license
  const devices = row?.devices ?? []
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
                <dt>客户编号</dt>
                <dd>{row.customerNumber}</dd>
                <dt>授权编号</dt>
                <dd>{licenseNumber(row.id)}</dd>
                <dt>期限状态</dt>
                <dd>{terms[row.termState]}</dd>
                <dt>授权期限</dt>
                <dd>{row.term ? termText(row.term) : row.termState === "perpetual" ? "永久" : "—"}</dd>
                <dt>生效时间</dt>
                <dd>{row.notBefore ? dateText(row.notBefore) : "首次成功激活时起算"}</dd>
                <dt>截止时间</dt>
                <dd>{row.termState === "unactivated" ? "首次激活后按授权期限计算" : dateText(row.expiresAt)}</dd>
                <dt>设备数量</dt>
                <dd>{row.deviceLimit} 台</dd>
                <dt>当前版本</dt>
                <dd>{row.revision}</dd>
              </dl>
              <div className="flex gap-3">
                <Button variant="outline" disabled={codeBusy} onClick={() => void toggleCode()}>
                  {codeBusy ? "正在读取…" : codeVisible ? "隐藏授权码" : "显示授权码"}
                </Button>
                {codeVisible && <Button onClick={() => void copyCode()}>复制授权码</Button>}
                {onManage && (
                  <Button
                    variant="outline"
                    disabled={
                      row.termState === "unactivated" ||
                      row.expiresAt === null ||
                      !row.issuanceEnabled ||
                      !row.customerIssuanceEnabled
                    }
                    onClick={() => onManage(row, "renew")}
                  >
                    续期
                  </Button>
                )}
              </div>
              <IssuanceStateForm
                kind="license"
                id={row.id}
                enabled={row.issuanceEnabled}
                revision={row.revision}
                onChanged={() => {
                  setReload((value) => value + 1)
                  onChanged?.()
                }}
              />
              <section className="grid gap-3">
                <h3 className="font-semibold">历次签发</h3>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>版本</TableHead>
                      <TableHead>动作</TableHead>
                      <TableHead>签发时间</TableHead>
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
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </section>
              <section className="grid gap-3">
                <h3 className="font-semibold">设备记录</h3>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>设备名称</TableHead>
                      <TableHead>客户端版本</TableHead>
                      <TableHead>最近上报</TableHead>
                      <TableHead>状态</TableHead>
                      {onManage && <TableHead>操作</TableHead>}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {devices.map((value) => (
                      <TableRow key={value.bindingId}>
                        <TableCell>{value.deviceLabel || "未命名设备"}</TableCell>
                        <TableCell>
                          {[value.appVersion, value.buildId].filter(Boolean).join(" · ") || "尚未上报"}
                        </TableCell>
                        <TableCell>{value.lastReportedAt ? dateText(value.lastReportedAt) : "尚未上报"}</TableCell>
                        <TableCell>{value.deviceState === "current" ? "当前设备" : "已换机"}</TableCell>
                        {onManage && (
                          <TableCell>
                            {value.deviceState === "current" && row.termState !== "expired" && (
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={!row.issuanceEnabled || !row.customerIssuanceEnabled}
                                onClick={() => onManage(row, "transfer", value.bindingId)}
                              >
                                办理换机
                              </Button>
                            )}
                          </TableCell>
                        )}
                      </TableRow>
                    ))}
                    {!devices.length && (
                      <TableRow>
                        <TableCell colSpan={onManage ? 5 : 4} className="text-muted-foreground">
                          尚未绑定设备
                        </TableCell>
                      </TableRow>
                    )}
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
