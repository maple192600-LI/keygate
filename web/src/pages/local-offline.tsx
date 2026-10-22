import { useEffect, useState, type FormEvent, type ReactNode } from "react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { LayoutDashboard, Users, KeyRound, Send, Settings, LogOut } from "lucide-react"
import { localRequest, LocalAPIError, type LocalOwner } from "@/lib/local-offline-api"
import "./local-offline.css"
import LocalCustomers, {type CommercialCustomer} from "./local-customers"
import LocalSigningIdentity from "./local-signing"
import LocalIssue from "./local-issue"
import LocalLicenses,{type OfflineLicense} from "./local-licenses"
import LocalOverview from "./local-overview"
import LocalDataSettings,{MerchantSettings,LocalOperationHistory} from "./local-data-settings"

const attribution = <a className="text-sm text-muted-foreground" href="https://keygate.app" target="_blank" rel="noreferrer">Powered by Keygate</a>
function Field({ name, label, children }: { name: string; label: string; children: ReactNode }) { return <div className="grid gap-2"><Label htmlFor={name}>{label}</Label>{children}</div> }
const message = (error: unknown) => error instanceof Error ? error.message : "操作失败，请重试"

export default function LocalOfflineApp() {
  const [mode, setMode] = useState<"loading" | "setup" | "login" | "ready" | "error">("loading")
  const [owner, setOwner] = useState<LocalOwner | null>(null)
  const [error, setError] = useState("")
  const [reload, setReload] = useState(0)
  const [issuing,setIssuing] = useState<{customer:CommercialCustomer;existing?:{id:string;revision:number;action:"renew"|"transfer"}}|null>(null)
  const [page, setPage] = useState<"overview"|"customers"|"licenses"|"delivery"|"settings">("customers")
  const [epoch,setEpoch]=useState(0),[expiring,setExpiring]=useState(false),[termFilter,setTermFilter]=useState("")
  useEffect(() => {
    let alive = true
    localRequest<{ needed: boolean }>("/setup/status").then(async status => {
      if (!alive) return
      if (status.needed) { setMode("setup"); return }
      try { const user = await localRequest<LocalOwner>("/auth/me"); if (alive) { setOwner(user); setMode("ready") } }
      catch (cause) { if (!alive) return; if (cause instanceof LocalAPIError && cause.status === 401) setMode("login"); else { setError(message(cause)); setMode("error") } }
    }).catch(cause => { if (alive) { setError(message(cause)); setMode("error") } })
    return () => { alive = false }
  }, [reload])
  const signedOut = () => { setOwner(null);setIssuing(null);setMode("login") }
  const manageLicense=async(row:OfflineLicense,action:"renew"|"transfer")=>{
    try{const customer=await localRequest<CommercialCustomer>(`/admin/commercial-customers/${row.customerId}`);setIssuing({customer,existing:{id:row.id,revision:row.revision,action}})}catch(cause){setError(message(cause))}
  }
  const createLicense=()=>{setPage("customers");setExpiring(false);setTermFilter("")}
  return <div className="snail-local min-h-screen bg-muted/40 text-foreground">
    {mode === "loading" && <div role="status" className="p-8">正在打开管理端…</div>}
    {mode === "error" && <div className="mx-auto grid max-w-md gap-4 p-8"><div role="alert">{error}</div><Button onClick={() => { setMode("loading"); setReload(value => value + 1) }}>重新连接</Button>{attribution}</div>}
    {(mode === "setup" || mode === "login") && <LoginForm setup={mode === "setup"} onSetup={() => setMode("login")} onLogin={user => { setOwner(user); setMode("ready") }} />}
    {mode === "ready" && owner && <div className="lc-shell">
      <aside className="lc-sidebar"><div className="lc-brand"><img src="/snail-icon.svg" alt=""/><strong>蜗牛客户与<br/>授权中心</strong></div><nav aria-label="管理导航">{([["overview","概览",LayoutDashboard],["customers","客户",Users],["licenses","授权",KeyRound],["delivery","交付记录",Send],["settings","设置",Settings]] as const).map(([key,label,Icon])=><Button key={key} variant="ghost" aria-current={page===key?"page":undefined} onClick={()=>{setPage(key);setExpiring(false);setTermFilter("");setError("")}}><Icon aria-hidden="true"/>{label}</Button>)}</nav><footer><Button variant="ghost" onClick={async()=>{try{await localRequest("/auth/logout",{});signedOut()}catch(cause){setError(message(cause))}}}><LogOut aria-hidden="true"/>退出登录</Button>{attribution}</footer></aside>
      <div className="lc-workspace"><header className="lc-topbar"><span>客户与授权管理<span className="lc-breadcrumb-separator">/</span>{{overview:"概览",customers:"客户",licenses:"授权",delivery:"交付记录",settings:"设置"}[page]}</span><span>{owner.username}</span></header><main className="lc-content">{error&&<div role="alert" className="mb-4 text-destructive">{error}</div>}
        {page==="customers"&&<LocalCustomers epoch={epoch} onIssue={customer=>setIssuing({customer})} onManage={manageLicense}/>}
        {(page==="licenses"||page==="delivery")&&<LocalLicenses key={`${page}-${expiring}-${termFilter}`} initialTermState={termFilter} delivery={page==="delivery"} initialExpiring={expiring} epoch={epoch} onCreate={createLicense} onManage={manageLicense}/>}
        {page==="overview"&&<LocalOverview onCreate={createLicense} onNavigate={(target,soon,term)=>{setPage(target);setExpiring(Boolean(soon));setTermFilter(term||"")}}/>}
        {page==="settings"&&<div className="lc-settings"><div className="lc-page-heading"><h1>设置</h1></div><div className="lc-settings-pair"><MerchantSettings/><AccountSettings onSignedOut={signedOut}/></div><details className="lc-signing-panel rounded-xl border bg-card"><summary className="cursor-pointer p-5 font-semibold">签发身份</summary><LocalSigningIdentity key={epoch}/></details><LocalDataSettings onImported={()=>setEpoch(value=>value+1)}/><LocalOperationHistory epoch={epoch}/></div>}
      </main></div>{issuing&&<LocalIssue customer={issuing.customer} existing={issuing.existing} onClose={()=>{setIssuing(null);setEpoch(value=>value+1)}} onSettings={()=>{setIssuing(null);setPage("settings")}}/>}
    </div>}

  </div>
}

function LoginForm({ setup, onSetup, onLogin }: { setup: boolean; onSetup: () => void; onLogin: (owner: LocalOwner) => void }) {
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [siteName, setSiteName] = useState("")
  const [contact, setContact] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState("")
  async function submit(event: FormEvent) {
    event.preventDefault(); if (busy) return; setError("")
    if (setup && password !== confirm) { setError("两次密码输入不一致"); return }
    setBusy(true)
    try {
      if (setup) { await localRequest("/local/setup", { username, password, siteName, contact }); setPassword(""); setConfirm(""); onSetup() }
      else onLogin(await localRequest<LocalOwner>("/auth/password/login", { username, password }))
    } catch (cause) { setError(message(cause)) } finally { setBusy(false) }
  }
  return <main className="mx-auto grid min-h-screen max-w-md content-center gap-6 px-6 py-10"><h1 className="text-2xl font-semibold">{setup ? "首次设置" : "蜗牛客户与授权中心"}</h1><form className="grid gap-5 rounded-xl border bg-card p-6" onSubmit={submit}>
    <Field name="username" label="管理用户名"><Input id="username" value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" required /></Field>
    <Field name="password" label="密码"><Input id="password" placeholder={setup?"至少10个字符":undefined} type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete={setup ? "new-password" : "current-password"} required /></Field>
    {setup && <><Field name="confirm" label="确认密码"><Input id="confirm" type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" required /></Field><Field name="siteName" label="商家名称"><Input id="siteName" value={siteName} onChange={e => setSiteName(e.target.value)} required /></Field><Field name="contact" label="联系方式"><Input id="contact" value={contact} onChange={e => setContact(e.target.value)} required /></Field></>}
    {error && <div role="alert" className="text-sm text-destructive">{error}</div>}<Button disabled={busy} type="submit">{busy ? "正在提交…" : setup ? "完成设置" : "登录"}</Button>
  </form><footer>{attribution}</footer></main>
}

function AccountSettings({ onSignedOut }: { onSignedOut: () => void }) {
  const [currentPassword, setCurrent] = useState("")
  const [newPassword, setNew] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)
  async function submit(event: FormEvent) { event.preventDefault(); if (busy) return; setError(""); if (newPassword !== confirm) { setError("两次密码输入不一致"); return }; setBusy(true); try { await localRequest("/auth/password/change", { currentPassword, newPassword }); onSignedOut() } catch (cause) { setError(message(cause)) } finally { setBusy(false) } }
  return <section className="max-w-lg rounded-xl border bg-card p-6"><h2 className="mb-6 text-lg font-semibold">修改管理密码</h2><form className="grid gap-5" onSubmit={submit}><Field name="currentPassword" label="当前密码"><Input id="currentPassword" type="password" value={currentPassword} onChange={e => setCurrent(e.target.value)} autoComplete="current-password" required /></Field><Field name="newPassword" label="新密码"><Input id="newPassword" placeholder="至少10个字符" type="password" value={newPassword} onChange={e => setNew(e.target.value)} autoComplete="new-password" required /></Field><Field name="confirmNew" label="确认新密码"><Input id="confirmNew" type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" required /></Field>{error && <div role="alert" className="text-sm text-destructive">{error}</div>}<Button className="justify-self-start" type="submit" disabled={busy}>{busy ? "正在保存…" : "保存密码"}</Button></form></section>
}
