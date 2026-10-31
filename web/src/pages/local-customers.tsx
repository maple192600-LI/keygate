import { Plus, RefreshCw, Search, Users } from "lucide-react"
import { type FormEvent, useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { localRequest } from "@/lib/local-offline-api"
import LocalLicenses from "./local-licenses"
import IssuanceStateForm from "./local-state-form"

export type CommercialCustomer = {
  id: string
  displayName: string
  customerType: "company" | "person"
  contactName: string
  contactMethod: string
  email: string | null
  sourceText: string
  notes: string
  issuanceEnabled: boolean
  licenseCount: number
  createdAt: string
}
type Draft = Pick<
  CommercialCustomer,
  "displayName" | "customerType" | "contactName" | "contactMethod" | "email" | "sourceText" | "notes"
>
const empty: Draft = {
  displayName: "",
  customerType: "company",
  contactName: "",
  contactMethod: "",
  email: null,
  sourceText: "",
  notes: "",
}
const customerNumber = (id: string) => `C-${id.replaceAll("-", "").toUpperCase()}`

export default function LocalCustomers({
  onIssue,
  onSelectLicense,
  hidden = false,
  epoch = 0,
}: {
  epoch?: number
  onIssue?: (customer: CommercialCustomer, from?: "customer-detail") => void
  onSelectLicense: (id: string) => void
  hidden?: boolean
}) {
  const [items, setItems] = useState<CommercialCustomer[]>([])
  const [total, setTotal] = useState(0)
  const [query, setQuery] = useState("")
  const [submitted, setSubmitted] = useState("")
  const [page, setPage] = useState(1)
  const [reload, setReload] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [editing, setEditing] = useState<CommercialCustomer | "new" | null>(null)
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload and epoch intentionally re-fetch the current customer page.
  useEffect(() => {
    let alive = true
    setBusy(true)
    localRequest<{ items: CommercialCustomer[]; total: number }>(
      `/admin/commercial-customers?q=${encodeURIComponent(submitted)}&page=${page}`,
    )
      .then((result) => {
        if (alive) {
          setItems(result.items)
          setTotal(result.total)
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
  }, [page, submitted, reload, epoch])
  if (editing) {
    return (
      <CustomerForm
        customer={editing}
        epoch={epoch}
        hidden={hidden}
        onIssue={onIssue}
        onSelectLicense={onSelectLicense}
        onClose={() => {
          setEditing(null)
          setReload((value) => value + 1)
        }}
        onSaved={(saved) => {
          setEditing(editing === "new" ? null : saved)
          setReload((value) => value + 1)
        }}
      />
    )
  }
  return (
    <section className="lc-list-page" hidden={hidden}>
      <div className="lc-page-heading">
        <h1>客户</h1>
        <Button onClick={() => setEditing("new")}>
          <Plus aria-hidden="true" />
          新建客户
        </Button>
      </div>
      {error && (
        <div role="alert" className="text-destructive">
          {error}
        </div>
      )}
      <div className="lc-list-panel">
        <form
          className="lc-toolbar"
          onSubmit={(event) => {
            event.preventDefault()
            setSubmitted(query)
            setPage(1)
          }}
        >
          <div className="lc-search">
            <Search aria-hidden="true" />
            <Input
              aria-label="查询客户"
              placeholder="客户、联系人、联系方式、客户编号或授权编号"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <Button type="submit" variant="outline">
            查询
          </Button>
          <Button type="button" variant="ghost" onClick={() => setReload((value) => value + 1)} disabled={busy}>
            <RefreshCw aria-hidden="true" />
            刷新
          </Button>
        </form>
        <Table className="lc-customers-table">
          <TableHeader>
            <TableRow>
              <TableHead>客户名称</TableHead>
              <TableHead>联系人</TableHead>
              <TableHead>联系方式</TableHead>
              <TableHead>来源／介绍人</TableHead>
              <TableHead>授权数</TableHead>
              <TableHead>登记日期</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((customer) => (
              <TableRow key={customer.id}>
                <TableCell>
                  <button
                    type="button"
                    className="lc-text-action lc-customer-name"
                    onClick={() => setEditing(customer)}
                  >
                    {customer.displayName}
                  </button>
                  <div className="text-xs text-muted-foreground">{customerNumber(customer.id)}</div>
                </TableCell>
                <TableCell>{customer.contactName || "—"}</TableCell>
                <TableCell>{customer.contactMethod || customer.email || "—"}</TableCell>
                <TableCell>{customer.sourceText || "—"}</TableCell>
                <TableCell>
                  <span className="lc-count">{customer.licenseCount}</span>
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", dateStyle: "medium" }).format(
                    new Date(customer.createdAt),
                  )}
                </TableCell>
                <TableCell>
                  <div className="lc-row-actions">
                    <Button variant="link" size="sm" onClick={() => setEditing(customer)}>
                      查看档案
                    </Button>
                    {onIssue && (
                      <Button
                        variant="link"
                        size="sm"
                        disabled={!customer.issuanceEnabled}
                        onClick={() => onIssue(customer)}
                      >
                        发放授权
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={7}>
                  <div className="lc-empty" role="status">
                    <Users aria-hidden="true" />
                    <span>
                      {busy ? "正在读取客户…" : error ? "客户未能读取" : submitted ? "没有找到匹配的客户" : "暂无客户"}
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        <div className="lc-pagination">
          <span>共 {total} 位客户</span>
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
      </div>
    </section>
  )
}
export function CustomerForm({
  customer,
  epoch = 0,
  returnLabel,
  hidden = false,
  onClose,
  onSaved,
  onIssue,
  onSelectLicense,
}: {
  customer: CommercialCustomer | "new"
  epoch?: number
  returnLabel?: string
  hidden?: boolean
  onClose: () => void
  onSaved: (customer: CommercialCustomer) => void
  onIssue?: (customer: CommercialCustomer, from?: "customer-detail") => void
  onSelectLicense: (id: string) => void
}) {
  const [draft, setDraft] = useState<Draft>(
    customer === "new"
      ? empty
      : {
          displayName: customer.displayName,
          customerType: customer.customerType,
          contactName: customer.contactName,
          contactMethod: customer.contactMethod,
          email: customer.email,
          sourceText: customer.sourceText,
          notes: customer.notes,
        },
  )
  const [enabled, setEnabled] = useState(customer === "new" ? true : customer.issuanceEnabled)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [sameName, setSameName] = useState(false)
  async function checkName() {
    if (!draft.displayName.trim()) return
    try {
      const matches = await localRequest<{ items: CommercialCustomer[] }>(
        `/admin/commercial-customers?q=${encodeURIComponent(draft.displayName)}&pageSize=100`,
      )
      setSameName(
        matches.items.some(
          (item) => item.displayName === draft.displayName.trim() && (customer === "new" || customer.id !== item.id),
        ),
      )
    } catch {
      /* Saving still reports any service failure. */
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError("")
    try {
      const saved = await localRequest<CommercialCustomer>(
        `/admin/commercial-customers${customer === "new" ? "" : `/${customer.id}`}`,
        draft,
        customer === "new" ? "POST" : "PATCH",
      )
      setDraft({
        displayName: saved.displayName,
        customerType: saved.customerType,
        contactName: saved.contactName,
        contactMethod: saved.contactMethod,
        email: saved.email,
        sourceText: saved.sourceText,
        notes: saved.notes,
      })
      setEnabled(saved.issuanceEnabled)
      onSaved(saved)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "保存失败，请重试")
    } finally {
      setBusy(false)
    }
  }
  const content = (
    <>
      <form className="lc-form-grid grid grid-cols-2 gap-5" onSubmit={submit}>
        <div className="grid gap-2">
          <Label htmlFor="customer-name">客户名称</Label>
          <Input
            id="customer-name"
            value={draft.displayName}
            maxLength={100}
            onBlur={() => void checkName()}
            onChange={(event) => setDraft({ ...draft, displayName: event.target.value })}
            required
          />
          {sameName && <div className="text-sm text-muted-foreground">已有同名客户，请核对档案。</div>}
        </div>
        <div className="grid gap-2">
          <Label htmlFor="customer-type">客户类型</Label>
          <select
            id="customer-type"
            className="h-9 rounded-md border border-input bg-transparent px-3"
            value={draft.customerType}
            onChange={(event) => setDraft({ ...draft, customerType: event.target.value as Draft["customerType"] })}
          >
            <option value="company">企业</option>
            <option value="person">个人</option>
          </select>
        </div>
        {(
          [
            ["contactName", "联系人"],
            ["contactMethod", "联系方式"],
            ["email", "邮箱"],
            ["sourceText", "来源／介绍人"],
          ] as const
        ).map(([key, label]) => (
          <div key={key} className="grid gap-2">
            <Label htmlFor={`customer-${key}`}>{label}</Label>
            <Input
              id={`customer-${key}`}
              type={key === "email" ? "email" : "text"}
              value={draft[key] || ""}
              onChange={(event) => setDraft({ ...draft, [key]: event.target.value })}
            />
          </div>
        ))}
        <div className="col-span-2 grid gap-2">
          <Label htmlFor="customer-notes">备注</Label>
          <Input
            id="customer-notes"
            value={draft.notes}
            onChange={(event) => setDraft({ ...draft, notes: event.target.value })}
          />
        </div>
        {error && (
          <div role="alert" className="col-span-2 text-sm text-destructive">
            {error}
          </div>
        )}
        <div className="col-span-2 flex gap-3">
          <Button type="submit" disabled={busy}>
            {busy ? "正在保存…" : "保存客户"}
          </Button>
        </div>
      </form>
      {customer !== "new" && (
        <div className="grid gap-6">
          {onIssue && (
            <div className="flex gap-3">
              <Button
                disabled={!enabled}
                onClick={() => {
                  onIssue({ ...customer, issuanceEnabled: enabled }, "customer-detail")
                }}
              >
                为此客户发放授权
              </Button>
            </div>
          )}
          <IssuanceStateForm
            kind="customer"
            id={customer.id}
            enabled={enabled}
            onChanged={() => setEnabled((value) => !value)}
          />
          <LocalLicenses
            epoch={epoch + Number(enabled)}
            customerId={customer.id}
            title="已有授权"
            onSelectLicense={onSelectLicense}
          />
        </div>
      )}
    </>
  )
  return (
    <section className="lc-list-page" hidden={hidden}>
      <div className="lc-page-heading">
        <div className="grid gap-1">
          <h1>{customer === "new" ? "新建客户" : "客户档案"}</h1>
          {customer !== "new" && <span className="text-sm text-muted-foreground">{customerNumber(customer.id)}</span>}
        </div>
        <Button variant="outline" type="button" onClick={onClose} disabled={busy}>
          {customer === "new" ? (returnLabel ?? "取消") : "返回列表"}
        </Button>
      </div>
      <div className="lc-list-panel grid gap-6 p-6">{content}</div>
    </section>
  )
}
