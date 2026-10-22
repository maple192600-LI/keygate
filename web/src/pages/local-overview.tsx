import {useEffect,useState} from "react"
import {Button} from "@/components/ui/button"
import {localRequest} from "@/lib/local-offline-api"
import LocalLicenses from "./local-licenses"
type Overview={customers:number;licenses:number;valid:number;expired:number;expiring:number;pendingDelivery:number;stopped:number;usageState:"unreported"}
export default function LocalOverview({onNavigate,onCreate}:{onNavigate:(target:"customers"|"licenses"|"delivery",expiring?:boolean)=>void;onCreate:()=>void}){
 const [state,setState]=useState<Overview|null>(null),[error,setError]=useState("")
 useEffect(()=>{let alive=true;localRequest<Overview>("/admin/overview").then(value=>{if(alive)setState(value)}).catch(cause=>{if(alive)setError(cause.message)});return()=>{alive=false}},[])
 return <section className="grid gap-6"><div className="flex items-center gap-3"><h1 className="mr-2 text-xl font-semibold">概览</h1><Button onClick={onCreate}>发放授权</Button></div>{error&&<div role="alert" className="text-destructive">{error}</div>}
  <div className="grid grid-cols-4 gap-4">{([['客户数',state?.customers,'customers'],['有效授权',state?.valid,'licenses'],['待交付',state?.pendingDelivery,'delivery'],['30天内到期',state?.expiring,'licenses']] as const).map(([label,count,target],index)=><Button key={label} variant="outline" className="grid h-auto justify-items-start gap-3 p-5" onClick={()=>onNavigate(target,index===3)}><span className="text-sm text-muted-foreground">{label}</span><strong className="text-3xl">{count??'—'}</strong></Button>)}</div>
  <LocalLicenses compact initialExpiring title="30天内到期"/>
 </section>
}
