import {useRef,useState} from "react"
import {Button} from "@/components/ui/button"
import {Input} from "@/components/ui/input"
import {Label} from "@/components/ui/label"
import {localRequest,LocalAPIError} from "@/lib/local-offline-api"
export default function IssuanceStateForm({kind,id,enabled,revision=0,onChanged}:{kind:"license"|"customer";id:string;enabled:boolean;revision?:number;onChanged:()=>void}){
 const [reason,setReason]=useState(""),[error,setError]=useState(""),[busy,setBusy]=useState(false),[uncertain,setUncertain]=useState(false)
 const attempt=useRef<{operationId:string;enabled:boolean;expectedEnabled:boolean;expectedRevision:number;reason:string}|null>(null)
 async function submit(){if(busy)return;setBusy(true);setError("");try{
  attempt.current??={operationId:crypto.randomUUID(),enabled:!enabled,expectedEnabled:enabled,expectedRevision:revision,reason}
  await localRequest(`/admin/${kind==="license"?"offline-licenses":"commercial-customers"}/${id}/issuance-state`,attempt.current)
  setReason("");setUncertain(false);attempt.current=null;onChanged()
 }catch(cause){setError((cause as Error).message);const unknown=cause instanceof LocalAPIError&&cause.status>=500;setUncertain(unknown);if(!unknown)attempt.current=null}finally{setBusy(false)}}
 return <section className="grid gap-3 border-t pt-4"><div className="flex items-center gap-3"><strong>{enabled?"可续发":"已停发"}</strong><span className="text-sm text-muted-foreground">已保存许可仍按原期限有效</span></div><Label htmlFor={`state-${kind}-${id}`}>调整原因</Label><div className="flex gap-3"><Input id={`state-${kind}-${id}`} value={reason} onChange={event=>setReason(event.target.value)} disabled={busy||uncertain} maxLength={1000}/><Button type="button" variant="outline" disabled={busy||!reason.trim()} onClick={()=>void submit()}>{busy?"正在保存…":uncertain?"按原操作重试":enabled?"停止续发":"恢复续发"}</Button></div>{error&&<div role="alert" className="text-sm text-destructive">{error}</div>}</section>
}
