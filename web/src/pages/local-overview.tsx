import { CalendarDays, ChevronRight, KeyRound, Plus, Send, Users } from "lucide-react"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { localRequest } from "@/lib/local-offline-api"
import LocalLicenses, { type LicenseAction } from "./local-licenses"

type Overview = {
  customers: number
  licenses: number
  valid: number
  expired: number
  expiring: number
  pendingDelivery: number
  stopped: number
  usageState: "unreported"
}
export default function LocalOverview({
  onNavigate,
  onCreate,
  onManage,
}: {
  onNavigate: (target: "customers" | "licenses" | "delivery", expiring?: boolean, term?: "active") => void
  onCreate: () => void
  onManage: LicenseAction
}) {
  const [state, setState] = useState<Overview | null>(null)
  const [error, setError] = useState("")
  useEffect(() => {
    let alive = true
    localRequest<Overview>("/admin/overview")
      .then((value) => {
        if (alive) setState(value)
      })
      .catch((cause) => {
        if (alive) setError(cause.message)
      })
    return () => {
      alive = false
    }
  }, [])
  const cards = [
    { label: "已登记客户", count: state?.customers, icon: Users, open: () => onNavigate("customers") },
    { label: "有效授权", count: state?.valid, icon: KeyRound, open: () => onNavigate("licenses", false, "active") },
    { label: "待交付文件", count: state?.pendingDelivery, icon: Send, open: () => onNavigate("delivery") },
    { label: "30天内到期", count: state?.expiring, icon: CalendarDays, open: () => onNavigate("licenses", true) },
  ]
  return (
    <section className="lc-overview">
      <div className="lc-page-heading">
        <h1>概览</h1>
        <Button onClick={onCreate}>
          <Plus aria-hidden="true" />
          发放授权
        </Button>
      </div>
      {error && (
        <div role="alert" className="text-destructive">
          {error}
        </div>
      )}
      <div className="lc-stat-grid">
        {cards.map(({ label, count, icon: Icon, open }) => (
          <button type="button" className="lc-stat-card" key={label} onClick={open}>
            <span>
              {label}
              <Icon aria-hidden="true" />
            </span>
            <strong>{count ?? "—"}</strong>
          </button>
        ))}
      </div>
      <div className="lc-overview-columns">
        <div className="lc-overview-lists">
          <LocalLicenses compact title="最近发放" onManage={onManage} />
          <LocalLicenses compact initialExpiring title="30天内到期" onManage={onManage} />
        </div>
        <aside className="lc-overview-aside">
          <section className="lc-task-panel">
            <h2>待处理</h2>
            <button type="button" onClick={() => onNavigate("delivery")}>
              <Send aria-hidden="true" />
              <span>{state?.pendingDelivery ?? "—"} 份文件待交付</span>
              <ChevronRight aria-hidden="true" />
            </button>
            <button type="button" onClick={() => onNavigate("licenses", true)}>
              <CalendarDays aria-hidden="true" />
              <span>{state?.expiring ?? "—"} 份授权即将到期</span>
              <ChevronRight aria-hidden="true" />
            </button>
          </section>
          <section className="lc-task-panel">
            <h2>授权状态</h2>
            <dl>
              <dt>累计授权</dt>
              <dd>{state?.licenses ?? "—"}</dd>
              <dt>已到期</dt>
              <dd>{state?.expired ?? "—"}</dd>
              <dt>已停发</dt>
              <dd>{state?.stopped ?? "—"}</dd>
              <dt>离线使用</dt>
              <dd>未回传</dd>
            </dl>
          </section>
        </aside>
      </div>
    </section>
  )
}
