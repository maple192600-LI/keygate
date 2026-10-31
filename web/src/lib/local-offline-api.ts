export class LocalAPIError extends Error {
  code: string
  status: number
  constructor(code: string, message: string, status: number) { super(message); this.code = code; this.status = status }
}
let refreshing: Promise<boolean> | null = null
async function localResponse(path: string, body?: unknown, method = body === undefined ? "GET" : "POST", retry = true, form?:FormData): Promise<Response> {
  let response: Response
  try { response = await fetch(`/api/v1${path}`, { method, credentials: "same-origin", cache: "no-store", headers: form?undefined:{ "Content-Type": "application/json" }, body: form||(body === undefined ? undefined : JSON.stringify(body)) }) }
  catch { throw new LocalAPIError("SERVICE_UNAVAILABLE", "本地管理服务暂时不可用，请重新连接后按原操作重试", 503) }
  if (response.status === 401 && retry && !["/auth/password/login", "/auth/refresh", "/local/setup"].includes(path)) {
    const denied = await response.clone().json().catch(() => null)
    if (denied?.error?.code !== "UNAUTHORIZED") return response
    refreshing ??= fetch("/api/v1/auth/refresh", { method: "POST", credentials: "same-origin" }).then(r => {
      if(r.status>=500 || r.status===429) throw new LocalAPIError("SERVICE_UNAVAILABLE", "登录续期暂未完成，请稍后重试", r.status)
      return r.ok
    }).finally(() => { refreshing = null })
    let refreshed = false
    try { refreshed = await refreshing } catch { throw new LocalAPIError("SERVICE_UNAVAILABLE", "本地管理服务暂时不可用，请稍后重试", 503) }
    if (refreshed) return localResponse(path, body, method, false,form)
  }
  return response
}
async function decoded<T>(response:Response):Promise<T> {
  let result: { success: boolean; data: T; error?: { code: string; message: string } }
  try { result = await response.json() } catch { throw new LocalAPIError("SERVICE_UNAVAILABLE", "本地管理服务返回异常，请稍后重试", response.status) }
  if (!response.ok || !result.success) throw new LocalAPIError(result.error?.code || "REQUEST_FAILED", response.status === 429 ? "操作过于频繁，请稍后重试" : result.error?.code === "INTERNAL_ERROR" ? "本地资料操作未完成，请稍后重试；已有资料保留" : result.error?.message || "操作失败，请重试", response.status)
  return result.data
}
export async function localRequest<T>(path:string,body?:unknown,method=body===undefined?"GET":"POST"):Promise<T> {return decoded<T>(await localResponse(path,body,method))}
export function downloadBlob(blob:Blob,name:string) {
  const url=URL.createObjectURL(blob),link=document.createElement("a")
  link.href=url;link.download=name;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000)
}
export async function localDownload(path:string,name:string,body?:unknown) {
  const response=await localResponse(path,body)
  if(!response.ok) {await decoded(response);return}
  downloadBlob(await response.blob(),name)
}
export async function localFileRequest<T>(path:string,file:File,password?:string):Promise<T> {
  if(file.size>128*1024*1024)throw new Error("文件超过允许大小，请选择完整原件")
  const form=new FormData();form.append("file",file)
  if(password!==undefined)form.append("password",password)
  return decoded<T>(await localResponse(path,undefined,"POST",true,form))
}
export async function readLocalFile(file:File,maxBytes:number):Promise<string> {
  if(file.size>maxBytes) throw new Error("文件超过允许大小，请选择完整原件")
  try {return new TextDecoder("utf-8",{fatal:true,ignoreBOM:true}).decode(await file.arrayBuffer())}
  catch {throw new Error("文件不是有效的 UTF-8 文本")}
}
export type LocalOwner = { id: string; username: string; name: string; email: string | null; role: "owner" }
