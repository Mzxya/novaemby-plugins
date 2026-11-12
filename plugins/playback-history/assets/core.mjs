export const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
export const nonnegative = value => Number.isFinite(Number(value))?Math.max(0,Number(value)):0;
export const count = value => nonnegative(value).toLocaleString("zh-CN");
export const hours = value => (nonnegative(value)/3600).toLocaleString("zh-CN",{maximumFractionDigits:1});
export function duration(value) { const seconds=Math.floor(nonnegative(value));if(seconds<60)return `${seconds} 秒`;const minutes=Math.floor(seconds/60);return minutes<60?`${minutes} 分钟`:`${Math.floor(minutes/60)} 小时 ${minutes%60} 分钟`; }
export const kindLabel = type => ({Movie:"电影",Episode:"剧集"}[type]||"其他视频");
export const basePath = path => path.replace(/\/$/,"");
export function csrf(cookie) {const match=String(cookie||"").match(/(?:^|;\s*)nova_csrf=([^;]*)/);if(!match)return "";try{return decodeURIComponent(match[1]);}catch{return "";}}
export function validZone(value) {try{if(typeof value!=="string"||!value)return "UTC";new Intl.DateTimeFormat("zh-CN",{timeZone:value});return value;}catch{return "UTC";}}
export function dateTime(value,zone="UTC",onlyDate=false) {const date=new Date(value);if(!value||!Number.isFinite(date.getTime()))return "—";return date.toLocaleString("zh-CN",{timeZone:validZone(zone),year:"numeric",month:"2-digit",day:"2-digit",...(!onlyDate?{hour:"2-digit",minute:"2-digit"}:{})});}
export function progress(record) {return record.runtimeTicks>0?Math.min(100,nonnegative(record.positionTicks)/record.runtimeTicks*100):0;}
export function progressLabel(record) {return record.runtimeTicks>0?`${Math.round(progress(record))}%`:record.positionTicks>0?`播放位置 ${duration(record.positionTicks/1e7)}`:"进度未知";}
export function recordTitle(record) {if(record.title)return record.title;let title=record.seriesName||"";if(title&&record.seasonNumber!=null&&record.episodeNumber!=null)title+=` S${String(record.seasonNumber).padStart(2,"0")}E${String(record.episodeNumber).padStart(2,"0")}`;if(title&&record.itemName&&record.itemName!==record.seriesName)title+=` · ${record.itemName}`;return title||record.itemName||"未知内容";}
export function posterSources(record,base,template="") {
  const urls=record.id?[`${base}/poster/${encodeURIComponent(record.id)}`]:[];
  if(!template||!template.includes("{id}")||!template.startsWith("/")||/^\/{2}/.test(template)||/[\\\r\n]/.test(template))return urls;
  const ids=(record.itemType==="Episode"||record.kind==="Episode"?[record.seriesId,record.itemId]:[record.itemId,record.seriesId]).filter(Boolean);
  for(const id of new Set(ids))urls.push(template.replaceAll("{id}",encodeURIComponent(id)));
  return [...new Set(urls)];
}
export function queryString(state) {const query=new URLSearchParams({mode:state.mode,sort:state.sort,page:String(state.page)});if(state.user)query.set("user",state.user);for(const [k,v]of Object.entries(state.filters))if(v)query.set(k,v);return query.toString();}
export function createAPI(fetch,base,readCookie) {
  return async function(path,{method="GET",body,signal}={}) {
    const headers=new Headers({Accept:"application/json"});if(body!==undefined)headers.set("Content-Type","application/json");
    if(!["GET","HEAD"].includes(method.toUpperCase())){const token=csrf(readCookie());if(token)headers.set("X-Nova-CSRF",token);}
    const response=await fetch(base+path,{method,headers,signal,credentials:"same-origin",...(body!==undefined?{body:JSON.stringify(body)}:{})});
    let data;try{data=await response.json();}catch{if(response.ok)throw new Error("服务器返回了无法读取的数据");}
    if(!response.ok){const defaults={401:"登录已失效，请回到 NovaEmby 登录后重试",403:"当前账号没有访问权限",503:"插件暂不可用，请检查是否已启用并激活"};throw new Error(data?.error||defaults[response.status]||`请求失败（${response.status}）`);}
    return data;
  };
}
// Each scope change owns a controller. Late responses never replace the newer view.
export function latestRequest() {let serial=0,current;return {begin(){current?.abort();const controller=new AbortController();current=controller;const id=++serial;return {signal:controller.signal,active:()=>id===serial,abort:()=>controller.abort()};},cancel(){serial++;current?.abort();}};}
export function nextImage(image,onFailure=()=>{}) {
  let sources;try{sources=JSON.parse(image.dataset.sources||"[]");}catch{sources=[];}
  let index=0,done=false;
  function next(){if(done)return;if(index>=sources.length){done=true;image.hidden=true;onFailure();return;}image.dataset.loaded="false";image.src=sources[index++];}
  image.addEventListener("load",()=>{if(!done)image.dataset.loaded="true";});image.addEventListener("error",next);next();
}
