import { Clock3, LogOut, RefreshCw, Settings, Users } from "lucide-react"
import { type FormEvent, type ReactNode, useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { LocalAPIError, type LocalOwner, localRequest } from "@/lib/local-offline-api"
import "./local-offline.css"
import LocalCustomers, { type CommercialCustomer, CustomerForm } from "./local-customers"
import LocalDataSettings, {
  LocalOperationHistory,
  LocalRecoverySettings,
  MerchantSettings,
} from "./local-data-settings"
import LocalIssue from "./local-issue"
import LocalLicenses, { type OfflineLicense } from "./local-licenses"
import LocalSigningIdentity from "./local-signing"

const attribution = (
  <a className="text-sm text-muted-foreground" href="https://keygate.app" target="_blank" rel="noreferrer">
    Powered by Keygate
  </a>
)
function Field({ name, label, children }: { name: string; label: string; children: ReactNode }) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={name}>{label}</Label>
      {children}
    </div>
  )
}
const message = (error: unknown) => (error instanceof Error ? error.message : "操作失败，请重试")

export default function LocalOfflineApp() {
  const [mode, setMode] = useState<"loading" | "setup" | "login" | "ready" | "error">("loading")
  const [owner, setOwner] = useState<LocalOwner | null>(null)
  const [error, setError] = useState("")
  const [reload, setReload] = useState(0)
  const [issuing, setIssuing] = useState<{
    customer: CommercialCustomer
    existing?: { id: string; revision: number; action: "renew" | "transfer"; bindingId?: string }
  } | null>(null)
  const [page, setPage] = useState<"customers" | "expiring" | "settings">("customers")
  const [settingsTab, setSettingsTab] = useState("basic")
  const [choosingCustomer, setChoosingCustomer] = useState(false),
    [creatingCustomer, setCreatingCustomer] = useState(false)
  const [epoch, setEpoch] = useState(0)
  useEffect(() => {
    document.title = "小云雀客户授权中心"
  }, [])
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload retries the initial connection after a service error.
  useEffect(() => {
    let alive = true
    localRequest<{ needed: boolean }>("/setup/status")
      .then(async (status) => {
        if (!alive) return
        if (status.needed) {
          setMode("setup")
          return
        }
        try {
          const user = await localRequest<LocalOwner>("/auth/me")
          if (alive) {
            setOwner(user)
            setMode("ready")
          }
        } catch (cause) {
          if (!alive) return
          if (cause instanceof LocalAPIError && cause.status === 401) setMode("login")
          else {
            setError(message(cause))
            setMode("error")
          }
        }
      })
      .catch((cause) => {
        if (alive) {
          setError(message(cause))
          setMode("error")
        }
      })
    return () => {
      alive = false
    }
  }, [reload])
  const signedOut = () => {
    setOwner(null)
    setIssuing(null)
    setMode("login")
  }
  const manageLicense = async (row: OfflineLicense, action: "renew" | "transfer", bindingId?: string) => {
    try {
      const customer = await localRequest<CommercialCustomer>(`/admin/commercial-customers/${row.customerId}`)
      setIssuing({
        customer,
        existing: { id: row.id, revision: row.revision, action, ...(bindingId ? { bindingId } : {}) },
      })
    } catch (cause) {
      setError(message(cause))
    }
  }
  return (
    <div className="snail-local min-h-screen bg-muted/40 text-foreground">
      {mode === "loading" && (
        <div role="status" className="p-8">
          正在打开管理端…
        </div>
      )}
      {mode === "error" && (
        <div className="mx-auto grid max-w-md gap-4 p-8">
          <div role="alert">{error}</div>
          <Button
            onClick={() => {
              setMode("loading")
              setReload((value) => value + 1)
            }}
          >
            重新连接
          </Button>
          {attribution}
        </div>
      )}
      {(mode === "setup" || mode === "login") && (
        <LoginForm
          setup={mode === "setup"}
          onSetup={() => setMode("login")}
          onLogin={(user) => {
            setOwner(user)
            setMode("ready")
          }}
        />
      )}
      {mode === "ready" && owner && (
        <div className="lc-shell">
          <aside className="lc-sidebar">
            <div className="lc-brand">
              <img src="/xiaoyunque-icon.svg" alt="" />
              <strong>小云雀客户授权中心</strong>
            </div>
            <nav aria-label="管理导航">
              {(
                [
                  ["customers", "客户", Users],
                  ["expiring", "即将到期", Clock3],
                  ["settings", "设置", Settings],
                ] as const
              ).map(([key, label, Icon]) => (
                <Button
                  key={key}
                  variant="ghost"
                  aria-current={page === key ? "page" : undefined}
                  onClick={() => {
                    setPage(key)
                    if (key === "settings") setSettingsTab("basic")
                    setError("")
                  }}
                >
                  <Icon aria-hidden="true" />
                  {label}
                </Button>
              ))}
            </nav>
            <footer>
              <Button
                variant="ghost"
                onClick={async () => {
                  try {
                    await localRequest("/auth/logout", {})
                    signedOut()
                  } catch (cause) {
                    setError(message(cause))
                  }
                }}
              >
                <LogOut aria-hidden="true" />
                退出登录
              </Button>
              {attribution}
            </footer>
          </aside>
          <div className="lc-workspace">
            <header className="lc-topbar">
              <span>
                客户与授权管理<span className="lc-breadcrumb-separator">/</span>
                {{ customers: "客户", expiring: "即将到期", settings: "设置" }[page]}
              </span>
              <span>{owner.username}</span>
            </header>
            <main className="lc-content">
              {error && (
                <div role="alert" className="mb-4 text-destructive">
                  {error}
                </div>
              )}
              {page === "customers" && (
                <LocalCustomers
                  epoch={epoch}
                  onIssue={(customer) => setIssuing({ customer })}
                  onManage={manageLicense}
                />
              )}
              {page === "expiring" && (
                <LocalLicenses
                  key={page}
                  initialExpiring
                  title="即将到期"
                  epoch={epoch}
                  onManage={manageLicense}
                  onChanged={() => setEpoch((value) => value + 1)}
                />
              )}
              {page === "settings" && (
                <div className="lc-settings">
                  <div className="lc-page-heading">
                    <h1>设置</h1>
                    <span className="lc-app-version">
                      版本 {import.meta.env.VITE_SNAIL_MANAGER_VERSION || "开发版"}
                    </span>
                  </div>
                  <div className="lc-settings-panel">
                    <div className="lc-settings-tabs" role="tablist" aria-label="设置分类">
                      {(
                        [
                          ["basic", "基本信息"],
                          ["service", "服务"],
                          ["data", "备份与数据"],
                          ["history", "操作记录"],
                        ] as const
                      ).map(([key, label]) => (
                        <button
                          type="button"
                          role="tab"
                          id={`settings-tab-${key}`}
                          aria-controls={`settings-panel-${key}`}
                          aria-selected={settingsTab === key}
                          tabIndex={settingsTab === key ? 0 : -1}
                          key={key}
                          onClick={() => setSettingsTab(key)}
                          onKeyDown={(event) => {
                            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return
                            event.preventDefault()
                            const keys = ["basic", "service", "data", "history"]
                            const position = keys.indexOf(key)
                            const next =
                              event.key === "Home"
                                ? 0
                                : event.key === "End"
                                  ? 3
                                  : (position + (event.key === "ArrowRight" ? 1 : 3)) % 4
                            setSettingsTab(keys[next])
                            document.getElementById(`settings-tab-${keys[next]}`)?.focus()
                          }}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                    <div
                      role="tabpanel"
                      id="settings-panel-basic"
                      aria-labelledby="settings-tab-basic"
                      hidden={settingsTab !== "basic"}
                    >
                      <div className="lc-settings-pair">
                        <MerchantSettings />
                        <AccountSettings onSignedOut={signedOut} />
                        <LocalRecoverySettings />
                      </div>
                    </div>
                    <div
                      role="tabpanel"
                      id="settings-panel-service"
                      aria-labelledby="settings-tab-service"
                      hidden={settingsTab !== "service"}
                    >
                      <div className="grid gap-5">
                        <RuntimeStatus />
                        <LocalSigningIdentity key={epoch} />
                      </div>
                    </div>
                    <div
                      role="tabpanel"
                      id="settings-panel-data"
                      aria-labelledby="settings-tab-data"
                      hidden={settingsTab !== "data"}
                    >
                      <LocalDataSettings onImported={() => setEpoch((value) => value + 1)} />
                    </div>
                    <div
                      role="tabpanel"
                      id="settings-panel-history"
                      aria-labelledby="settings-tab-history"
                      hidden={settingsTab !== "history"}
                    >
                      {settingsTab === "history" && <LocalOperationHistory epoch={epoch} />}
                    </div>
                  </div>
                </div>
              )}
            </main>
          </div>
          {choosingCustomer && (
            <IssueCustomerPicker
              onClose={() => setChoosingCustomer(false)}
              onSelect={(customer) => {
                setChoosingCustomer(false)
                setIssuing({ customer })
              }}
              onNew={() => {
                setChoosingCustomer(false)
                setCreatingCustomer(true)
              }}
            />
          )}
          {creatingCustomer && (
            <CustomerForm
              customer="new"
              onClose={() => setCreatingCustomer(false)}
              onSaved={(customer) => {
                setCreatingCustomer(false)
                setEpoch((value) => value + 1)
                setIssuing({ customer })
              }}
            />
          )}
          {issuing && (
            <LocalIssue
              customer={issuing.customer}
              existing={issuing.existing}
              onClose={() => {
                setIssuing(null)
                setEpoch((value) => value + 1)
              }}
            />
          )}
        </div>
      )}
    </div>
  )
}

function LoginForm({
  setup,
  onSetup,
  onLogin,
}: {
  setup: boolean
  onSetup: () => void
  onLogin: (owner: LocalOwner) => void
}) {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [siteName, setSiteName] = useState("")
  const [contact, setContact] = useState("")
  const [recoveryCredential, setRecoveryCredential] = useState("")
  const [recovering, setRecovering] = useState(false)
  const [newPassword, setNewPassword] = useState("")
  const [confirmNewPassword, setConfirmNewPassword] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setError("")
    setNotice("")
    if (recovering) {
      if (newPassword !== confirmNewPassword) {
        setError("两次新密码输入不一致")
        return
      }
      setBusy(true)
      try {
        await localRequest("/local/recovery/reset", { username, recoveryCredential, newPassword })
        setRecoveryCredential("")
        setNewPassword("")
        setConfirmNewPassword("")
        setPassword("")
        setRecovering(false)
        setNotice("密码已更新，请使用新密码登录")
      } catch (cause) {
        setError(message(cause))
      } finally {
        setBusy(false)
      }
      return
    }
    if (setup && password !== confirm) {
      setError("两次密码输入不一致")
      return
    }
    setBusy(true)
    try {
      if (setup) {
        const result = await localRequest<{ recoveryCredential: string }>("/local/setup", {
          username,
          password,
          siteName,
          contact,
        })
        setPassword("")
        setConfirm("")
        setRecoveryCredential(result.recoveryCredential)
      } else onLogin(await localRequest<LocalOwner>("/auth/password/login", { username, password }))
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  if (setup && recoveryCredential) {
    return (
      <main className="mx-auto grid min-h-screen max-w-md content-center gap-6 px-6 py-10">
        <h1 className="text-2xl font-semibold">保存账号恢复凭据</h1>
        <section className="grid gap-4 rounded-xl border bg-card p-6">
          <div>此凭据仅显示一次。请保存在安全位置；遗失后可登录设置重新生成。</div>
          <code className="select-all break-all rounded border bg-muted p-3">{recoveryCredential}</code>
          <Button
            variant="outline"
            onClick={() =>
              void navigator.clipboard.writeText(recoveryCredential).catch((cause) => setError(message(cause)))
            }
          >
            复制恢复凭据
          </Button>
          {error && (
            <div role="alert" className="text-sm text-destructive">
              {error}
            </div>
          )}
          <Button
            onClick={() => {
              setRecoveryCredential("")
              onSetup()
            }}
          >
            已妥善保存，继续登录
          </Button>
        </section>
        {attribution}
      </main>
    )
  }
  return (
    <main className="mx-auto grid min-h-screen max-w-md content-center gap-6 px-6 py-10">
      <h1 className="text-2xl font-semibold">{setup ? "首次设置" : "小云雀客户授权中心"}</h1>
      <form className="grid gap-5 rounded-xl border bg-card p-6" onSubmit={submit}>
        <Field name="username" label="管理用户名">
          <Input
            id="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            required
          />
        </Field>
        {recovering ? (
          <>
            <Field name="recoveryCredential" label="恢复凭据">
              <Input
                id="recoveryCredential"
                value={recoveryCredential}
                onChange={(event) => setRecoveryCredential(event.target.value)}
                autoComplete="off"
                required
              />
            </Field>
            <Field name="newPassword" label="新密码">
              <Input
                id="newPassword"
                type="password"
                value={newPassword}
                onChange={(event) => setNewPassword(event.target.value)}
                autoComplete="new-password"
                minLength={10}
                maxLength={128}
                placeholder="至少10个字符"
                required
              />
            </Field>
            <Field name="confirmNewPassword" label="确认新密码">
              <Input
                id="confirmNewPassword"
                type="password"
                value={confirmNewPassword}
                onChange={(event) => setConfirmNewPassword(event.target.value)}
                autoComplete="new-password"
                minLength={10}
                maxLength={128}
                required
              />
            </Field>
          </>
        ) : (
          <Field name="password" label="密码">
            <Input
              id="password"
              placeholder={setup ? "至少10个字符" : undefined}
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete={setup ? "new-password" : "current-password"}
              required
            />
          </Field>
        )}
        {setup && !recovering && (
          <>
            <Field name="confirm" label="确认密码">
              <Input
                id="confirm"
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                required
              />
            </Field>
            <Field name="siteName" label="商家名称">
              <Input id="siteName" value={siteName} onChange={(e) => setSiteName(e.target.value)} required />
            </Field>
            <Field name="contact" label="联系方式">
              <Input id="contact" value={contact} onChange={(e) => setContact(e.target.value)} required />
            </Field>
          </>
        )}
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
        <Button disabled={busy} type="submit">
          {busy ? "正在提交…" : recovering ? "重置密码" : setup ? "完成设置" : "登录"}
        </Button>
        {!setup && (
          <Button
            type="button"
            variant="link"
            className="justify-self-start"
            onClick={() => {
              setRecovering(!recovering)
              setRecoveryCredential("")
              setNewPassword("")
              setConfirmNewPassword("")
              setError("")
              setNotice("")
            }}
          >
            {recovering ? "返回登录" : "忘记密码"}
          </Button>
        )}
      </form>
      <footer>{attribution}</footer>
    </main>
  )
}

function AccountSettings({ onSignedOut }: { onSignedOut: () => void }) {
  const [currentPassword, setCurrent] = useState("")
  const [newPassword, setNew] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setError("")
    if (newPassword !== confirm) {
      setError("两次密码输入不一致")
      return
    }
    setBusy(true)
    try {
      await localRequest("/auth/password/change", { currentPassword, newPassword })
      onSignedOut()
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="max-w-lg rounded-xl border bg-card p-6">
      <h2 className="mb-6 text-lg font-semibold">修改管理密码</h2>
      <form className="grid gap-5" onSubmit={submit}>
        <Field name="currentPassword" label="当前密码">
          <Input
            id="currentPassword"
            type="password"
            value={currentPassword}
            onChange={(e) => setCurrent(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>
        <Field name="newPassword" label="新密码">
          <Input
            id="newPassword"
            placeholder="至少10个字符"
            type="password"
            value={newPassword}
            onChange={(e) => setNew(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        <Field name="confirmNew" label="确认新密码">
          <Input
            id="confirmNew"
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>
        {error && (
          <div role="alert" className="text-sm text-destructive">
            {error}
          </div>
        )}
        <Button className="justify-self-start" type="submit" disabled={busy}>
          {busy ? "正在保存…" : "保存密码"}
        </Button>
      </form>
    </section>
  )
}

function IssueCustomerPicker({
  onClose,
  onSelect,
  onNew,
}: {
  onClose: () => void
  onSelect: (customer: CommercialCustomer) => void
  onNew: () => void
}) {
  const [query, setQuery] = useState(""),
    [submitted, setSubmitted] = useState(""),
    [reload, setReload] = useState(0),
    [items, setItems] = useState<CommercialCustomer[]>([]),
    [busy, setBusy] = useState(true),
    [error, setError] = useState("")
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload permits retrying an unchanged customer query after a failure.
  useEffect(() => {
    let alive = true
    setBusy(true)
    setError("")
    localRequest<{ items: CommercialCustomer[] }>(
      `/admin/commercial-customers?q=${encodeURIComponent(submitted)}&pageSize=100`,
    )
      .then((value) => {
        if (alive) {
          setItems(value.items)
          setError("")
        }
      })
      .catch((cause) => {
        if (alive) setError(message(cause))
      })
      .finally(() => {
        if (alive) setBusy(false)
      })
    return () => {
      alive = false
    }
  }, [submitted, reload])
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="lc-dialog max-w-2xl">
        <DialogHeader>
          <DialogTitle>选择客户</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <form
            className="lc-customer-picker-search"
            onSubmit={(event) => {
              event.preventDefault()
              setSubmitted(query)
              setReload((value) => value + 1)
            }}
          >
            <Input
              aria-label="查询客户"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="客户、联系人、联系方式或来源"
            />
            <Button variant="outline" type="submit">
              查询
            </Button>
            <Button variant="outline" type="button" onClick={onNew}>
              新建客户
            </Button>
          </form>
          {error && (
            <div role="alert" className="text-destructive">
              {error}
            </div>
          )}
          <div className="lc-customer-options">
            {items.map((customer) => (
              <button
                type="button"
                key={customer.id}
                disabled={!customer.issuanceEnabled || busy || !!error}
                onClick={() => onSelect(customer)}
              >
                <strong>{customer.displayName}</strong>
                <span>
                  {[
                    customer.customerType === "company" ? "企业" : "个人",
                    customer.contactName,
                    customer.contactMethod,
                    customer.email,
                    customer.sourceText,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </span>
                {customer.notes && <span>{customer.notes}</span>}
                <span>
                  登记时间：
                  {new Intl.DateTimeFormat("zh-CN", {
                    timeZone: "Asia/Shanghai",
                    dateStyle: "medium",
                    timeStyle: "medium",
                    hour12: false,
                  }).format(new Date(customer.createdAt))}
                </span>
              </button>
            ))}
            {!items.length && (
              <div className="lc-empty" role="status">
                {busy ? "正在读取客户…" : error ? "客户未能读取" : "暂无可选客户"}
              </div>
            )}
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

function RuntimeStatus() {
  const [status, setStatus] = useState<{ state: "ready" | "unavailable"; message: string; checkedAt: Date } | null>(
    null,
  )
  const [busy, setBusy] = useState(false)
  async function refresh() {
    setBusy(true)
    try {
      const response = await fetch("/health", { cache: "no-store", signal: AbortSignal.timeout(3000) })
      const result = await response.json()
      if (!response.ok || !result.success || result.data?.deployment !== "local_offline") {
        throw new Error(result.error?.message || "本机授权服务不可用")
      }
      setStatus({ state: "ready", message: "服务与数据库运行正常", checkedAt: new Date() })
    } catch (cause) {
      setStatus({
        state: "unavailable",
        message: cause instanceof Error ? cause.message : "本机授权服务不可用",
        checkedAt: new Date(),
      })
    } finally {
      setBusy(false)
    }
  }
  // biome-ignore lint/correctness/useExhaustiveDependencies: load the actual service status when entering settings.
  useEffect(() => {
    void refresh()
  }, [])
  return (
    <section className="rounded-xl border bg-card p-6">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">授权服务</h2>
        <Button variant="outline" disabled={busy} onClick={() => void refresh()}>
          <RefreshCw aria-hidden="true" />
          {busy ? "正在检查…" : "刷新状态"}
        </Button>
      </div>
      <div role="status" aria-live="polite" className={status?.state === "unavailable" ? "text-destructive" : ""}>
        {status
          ? `${status.message} · ${status.checkedAt.toLocaleTimeString("zh-CN", { hour12: false, timeZone: "Asia/Shanghai" })}`
          : "正在读取服务状态…"}
      </div>
      <div className="mt-3 text-sm text-muted-foreground">固定 HTTPS 域名：待部署配置</div>
    </section>
  )
}
