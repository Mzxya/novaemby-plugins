import {escapeHTML as esc,nonnegative as num,count,hours,duration,kindLabel,basePath,validZone,dateTime,progress,progressLabel,recordTitle,posterSources,queryString,createAPI,latestRequest,nextImage} from "./core.mjs";

const $=id=>document.getElementById(id);
const BASE=basePath(location.pathname);
const api=createAPI(window.fetch.bind(window),BASE,()=>document.cookie);
const requests=latestRequest();
const state={screen:"overview",user:"",days:30,mode:"records",sort:"recent",page:1,pages:1,filters:{q:"",type:"",status:"",from:"",to:""},boot:null,overview:null,rows:new Map(),busy:false,writing:false,zone:"UTC",trendDate:"",detailRequest:null};
const filmIcon='<svg viewBox="0 0 32 32" aria-hidden="true"><rect x="5" y="4" width="22" height="24" rx="3"/><path d="M10 4v24M22 4v24M5 11h5m12 0h5M5 21h5m12 0h5"/></svg>';
const time=value=>dateTime(value,state.zone);
const username=()=>state.boot?.users?.find(user=>user.key===state.user)?.name||(state.user?"所选用户":"全部用户");
function empty(message){return `<div class="mini-empty">${esc(message)}</div>`;}
function cover(record,badge="") {
  const title=recordTitle(record),urls=posterSources(record,BASE,state.boot?.posterTemplate);
  return `<span class="cover"><span class="cover-placeholder">${filmIcon}<strong>${esc(title)}</strong><small>${esc(kindLabel(record.itemType||record.kind))}</small></span>${urls.length?`<img data-sources="${esc(JSON.stringify(urls))}" alt="" loading="lazy" decoding="async">`:""}${badge?`<span class="cover-badge">${esc(badge)}</span>`:""}</span>`;
}
function images(root=document){root.querySelectorAll("img[data-sources]").forEach(img=>{if(img.dataset.bound)return;img.dataset.bound="true";nextImage(img,()=>{$("artworkNotice").hidden=false;});});}
function bars(root=document){root.querySelectorAll("[data-fill]").forEach(el=>el.style.setProperty("--fill",`${Math.min(100,num(el.dataset.fill))}%`));root.querySelectorAll("[data-bar]").forEach(el=>el.style.setProperty("--bar",`${Math.min(100,num(el.dataset.bar))}%`));}
let toastTimer;
function toast(message){clearTimeout(toastTimer);$("toast").textContent=message;$("toast").hidden=false;toastTimer=setTimeout(()=>$("toast").hidden=true,5000);}
function selectButton(container,attribute,value){$(container).querySelectorAll(`[data-${attribute}]`).forEach(button=>{const selected=button.dataset[attribute]===String(value);button.classList.toggle("selected",selected);button.setAttribute("aria-pressed",String(selected));});}
function bootstrap(data){
  state.boot=data;state.zone=validZone(data.timeZone);
  $("zone").textContent=`时区 ${state.zone}`;$("version").textContent=data.version?`v${data.version}`:"";
  $("user").innerHTML='<option value="">全部用户</option>'+(data.users||[]).map(user=>`<option value="${esc(user.key)}">${esc(user.name||"未知用户")}${user.deleted?"（已删除）":""}</option>`).join("");
  if(state.user&&!(data.users||[]).some(user=>user.key===state.user))$("user").insertAdjacentHTML("beforeend",`<option value="${esc(state.user)}">所选用户</option>`);
  $("user").value=state.user;
  $("diagnostic").hidden=!data.unsupportedEvents;$("diagnostic").textContent=data.unsupportedEvents?`有 ${count(data.unsupportedEvents)} 条事件缺少可识别的会话信息，未加入统计。请检查宿主事件格式。`:"";
}
async function load() {
  if(state.writing)return;
  const request=requests.begin(),timeout=setTimeout(request.abort,20000);
  const screen=state.screen;state.busy=true;$("refresh").disabled=true;$("loading").hidden=false;$("error").hidden=true;
  $("main").setAttribute("aria-busy","true");
  const query=new URLSearchParams({days:String(state.days)});if(state.user)query.set("user",state.user);
  const path=screen==="overview"?`/overview?${query}`:`/records?${queryString(state)}`;
  try {
    const [metadata,content]=await Promise.allSettled([api("/bootstrap",{signal:request.signal}),api(path,{signal:request.signal})]);
    if(!request.active())return;
    if(metadata.status==="fulfilled")bootstrap(metadata.value);
    if(content.status==="rejected")throw content.reason;
    if(screen==="overview")renderOverview(content.value);else renderRecords(content.value);
    if(metadata.status==="rejected"){ $("diagnostic").hidden=false;$("diagnostic").textContent="部分设置信息暂时不可用，已保留上次用户与封面配置。"; }
    $("connection").textContent="已同步";$("connection").dataset.state="online";
    $("updated").textContent=`更新于 ${time(new Date().toISOString())}`;
  } catch(error) {
    if(!request.active())return;
    $("errorText").textContent=(error.name==="AbortError"?"请求超时，请检查连接。":error.message)+" 当前保留上次成功显示的数据。";$("error").hidden=false;
    $("connection").textContent="连接中断";$("connection").dataset.state="offline";
  } finally {clearTimeout(timeout);if(request.active()){state.busy=false;$("refresh").disabled=false;$("loading").hidden=true;$("main").setAttribute("aria-busy","false");}}
}
function remember(rows){for(const row of rows||[])state.rows.set(row.id,row);}
function renderOverview(data) {
  state.rows.clear();state.overview=data;remember(data.recent);remember(data.active);remember(data.popular);
  state.zone=validZone(data.timeZone);const stats=data.stats||{},today=data.today||{};
  $("scope").textContent=`${username()} · 记录每一次播放`;
  $("rangeCaption").textContent=`${data.from} — ${data.to}`;
  $("plays").textContent=count(stats.plays);$("hours").textContent=hours(stats.seconds);$("contents").textContent=count(stats.contents);$("completed").textContent=count(stats.completed);
  $("todayPlays").textContent=`今日 ${count(today.plays)} 次`;$("todayHours").textContent=`今日 ${hours(today.seconds)} 小时 · 含暂停`;
  $("completionNote").textContent=stats.plays?`占所选播放的 ${Math.round(num(stats.completed)/stats.plays*100)}%`:"按播放进度判定";
  $("welcome").hidden=!(state.boot?.records===0&&!state.user);$("openCount").textContent=count(data.openCount);
  $("recent").innerHTML=(data.recent||[]).map(r=>`<button class="media-card" type="button" data-detail="${esc(r.id)}" aria-label="查看 ${esc(recordTitle(r))} 的详情">${cover(r,r.completed?"已看完":r.endedAt?"已结束":"未结束")}<span class="media-title">${esc(recordTitle(r))}</span><span class="media-meta">${esc(r.userName||"未知用户")}<br>${esc(time(r.startedAt))}</span></button>`).join("")||empty("所选范围内没有观看记录");
  renderTrend();renderTypes(data.kinds||{});renderRanking(data.users||[]);renderPopular(data.popular||[]);renderSessions(data.active||[]);bars();images();
}
function renderTrend(){
  const days=state.overview?.daily||[];const maximum=Math.max(1,...days.map(day=>num(day.seconds)));
  if(!days.some(day=>day.date===state.trendDate))state.trendDate=days.at(-1)?.date||"";
  $("chart").innerHTML=days.map(day=>`<button class="trend-bar${day.date===state.trendDate?" selected":""}" type="button" data-date="${esc(day.date)}" data-bar="${num(day.seconds)/maximum*100}" data-empty="${!day.seconds}" aria-pressed="${day.date===state.trendDate}" aria-label="${esc(day.date)}，${count(day.plays)} 次播放，${esc(duration(day.seconds))}" title="${esc(day.date)} · ${hours(day.seconds)} 小时"></button>`).join("");
  $("chartFrom").textContent=days[0]?.date||"";$("chartTo").textContent=days.at(-1)?.date||"";
  $("trendTotal").textContent=`合计 ${hours(state.overview?.stats?.seconds)} 小时`;
  $("trendDate").min=days[0]?.date||"";$("trendDate").max=days.at(-1)?.date||"";$("trendDate").value=state.trendDate;
  const selected=days.find(day=>day.date===state.trendDate);$("trendDetail").textContent=selected?`${count(selected.plays)} 次播放 · ${duration(selected.seconds)}`:"暂无数据";
}
function renderTypes(kinds){const total=Object.values(kinds).reduce((sum,n)=>sum+num(n),0);$("types").innerHTML=["Movie","Episode","Other"].map(kind=>{const plays=num(kinds[kind]);return `<div class="type-row" data-kind="${kind}"><span>${kindLabel(kind)}</span><strong>${count(plays)}<small>${total?Math.round(plays/total*100):0}%</small></strong><span class="type-track"><i data-fill="${total?plays/total*100:0}"></i></span></div>`;}).join("");}
function renderRanking(users){const maximum=Math.max(1,...users.map(user=>num(user.seconds)));$("ranking").innerHTML=users.map((user,i)=>`<button class="rank-row" type="button" data-user="${esc(user.key)}" aria-label="查看 ${esc(user.name)} 的记录"><span class="rank-number">${i+1}</span><span class="avatar">${esc(user.name?.slice(0,1)||"?")}</span><span class="rank-name"><strong>${esc(user.name)}</strong><span class="rank-track"><i data-fill="${num(user.seconds)/maximum*100}"></i></span></span><span class="rank-value">${hours(user.seconds)} 小时<small>${count(user.plays)} 次播放</small></span></button>`).join("")||empty("还没有用户观看数据");}
function renderPopular(items){$("popular").innerHTML=items.map(item=>`<button class="popular-row" type="button" data-detail="${esc(item.id)}">${cover(item)}<span class="popular-info"><strong>${esc(item.title)}</strong><small>${esc(kindLabel(item.kind))} · ${duration(item.seconds)}</small></span><span class="popular-count">${count(item.plays)}<small>次</small></span></button>`).join("")||empty("有了观看记录后，这里会显示常看的内容");}
const statusLabel=r=>r.endedAt?(r.completed?"已看完":"已结束"):({playing:"近期活跃",paused:"已暂停",stale:"状态待确认"}[r.status]||"未结束");
function renderSessions(rows){$("sessions").innerHTML=rows.map(r=>`<article class="session-card"><div class="session-top"><span class="avatar">${esc((r.userName||"?").slice(0,1))}</span><strong>${esc(r.userName||"未知用户")}</strong><span class="status-badge ${esc(r.status||"stale")}">${statusLabel(r)}</span></div><button class="session-title" type="button" data-detail="${esc(r.id)}">${esc(recordTitle(r))}</button><p class="session-meta">${esc([r.deviceName,r.client].filter(Boolean).join(" · ")||"设备信息未知")}</p><div class="progress-label"><span>${progressLabel(r)}</span><span>会话 ${duration(r.durationSeconds)}</span></div><div class="progress-track"><i data-fill="${progress(r)}"></i></div><p class="session-note">最后事件 ${esc(time(r.lastEventAt))}</p></article>`).join("")||empty("没有未结束的会话");}
function renderRecords(data){
  state.rows.clear();remember(data.rows);state.pages=data.pages||1;state.page=data.page||1;state.zone=validZone(data.timeZone);
  $("scope").textContent=`${username()} · 全部历史，按当前条件筛选`;
  $("listSummary").textContent=`${count(data.total)} ${state.mode==="items"?"项内容":"条记录"} · 会话 ${duration(data.stats?.seconds)} · 看完 ${count(data.stats?.completed)} 次`;
  let previous="";
  $("recordList").innerHTML=(data.rows||[]).map(r=>{
    const grouped=state.mode==="items",at=grouped?r.lastAt:r.startedAt,day=dateTime(at,state.zone,true);
    const divider=day!==previous&&state.sort!=="duration"&&state.sort!=="count"?`<h3 class="day-divider">${esc(day)}</h3>`:"";previous=day;
    return `${divider}<article class="record-row">${cover(r)}<div class="record-info"><button class="record-title" type="button" data-detail="${esc(r.id)}">${esc(recordTitle(r))}</button><div class="record-meta">${grouped?`<span>${count(r.plays)} 次播放</span><span>看完 ${count(r.completed)} 次</span>`:`<span>${esc(r.userName||"未知用户")}</span><span>${esc([r.deviceName,r.client].filter(Boolean).join(" · ")||"设备未知")}</span>`}</div><div class="record-bottom"><span>${grouped?kindLabel(r.kind):statusLabel(r)}</span>${!grouped?`<span class="progress-track record-progress"><i data-fill="${progress(r)}"></i></span><span>${progressLabel(r)}</span>`:""}</div></div><div class="record-aside"><span>${esc(time(at))}</span><span>${duration(grouped?r.seconds:r.durationSeconds)}</span>${$("manage").checked&&!grouped?`<button class="delete-record" type="button" data-delete="${esc(r.id)}">删除记录</button>`:""}</div></article>`;
  }).join("");
  $("recordList").hidden=!(data.rows||[]).length;$("listEmpty").hidden=!!(data.rows||[]).length;
  $("pageNumber").textContent=`${state.page} / ${state.pages}`;$("previous").disabled=state.page<=1;$("next").disabled=state.page>=state.pages;
  $("management").hidden=!$("manage").checked;$("clear").textContent=state.user?`清空 ${username()} 的全部历史`:"清空所有用户的全部历史";bars();images();
}
function screen(value){if(value!=="overview"&&value!=="records")return;state.screen=value;state.page=1;$("overviewView").hidden=value!=="overview";$("recordsView").hidden=value!=="records";selectButton("main","screen",value);$("manage").checked=false;return load();}
function filterSummary(){const active=Object.values(state.filters).filter(Boolean).length;$("filterCount").textContent=active?`(${active})`:"";}
function sortOptions(){const options=state.mode==="items"?[["recent","最近播放"],["count","播放次数"],["duration","累计时长"]]:[["recent","最近播放"],["oldest","最早播放"],["duration","会话时长"]];$("sort").innerHTML=options.map(([key,title])=>`<option value="${key}">${title}</option>`).join("");$("sort").value=state.sort;}
async function write(action,confirmation){
  if(state.writing||!window.confirm(confirmation))return;requests.cancel();state.writing=true;$("main").setAttribute("aria-busy","true");
  document.querySelectorAll(".delete-record,#clear").forEach(button=>button.disabled=true);
  try{await action();toast("记录已更新");}catch(error){toast(error.message);}finally{state.writing=false;$("main").setAttribute("aria-busy","false");document.querySelectorAll(".delete-record,#clear").forEach(button=>button.disabled=false);load();}
}
function deleteRecord(id){const row=state.rows.get(id);if(!row||!$("manage").checked)return;return write(()=>api(`/records/${encodeURIComponent(id)}`,{method:"DELETE"}),`永久删除「${recordTitle(row)}」这条播放记录？媒体文件不会被删除。`);}
function clearRecords(){if(!$("manage").checked)return;const user=state.user,name=username();return write(()=>api("/clear",{method:"POST",body:{user,confirm:true}}),`永久清空${name}的全部历史？\n这会忽略当前日期和搜索筛选，无法撤销。媒体文件不会被删除。`);}
function detailMarkup(record,item,available){
  const title=item?.name||recordTitle(record),fields=[["内容类型",kindLabel(item?.type||record.itemType||record.kind)],["年份",item?.year||record.year||"—"],["所属剧集",item?.seriesName||record.seriesName||"—"],["媒体总时长",item?.runtimeTicks||record.runtimeTicks?duration((item?.runtimeTicks||record.runtimeTicks)/1e7):"未知"],["最近播放",time(record.startedAt||record.lastAt)],["观看用户",record.userName||"—"]];
  if(item?.originalTitle)fields.push(["原始标题",item.originalTitle]);
  for(const [key,value]of Object.entries(item?.providerIds||{}))fields.push([key,value]);
  return `<div class="detail-hero">${cover(record)}<div><h2 id="detailTitle">${esc(title)}</h2><p class="detail-kind">${esc(kindLabel(item?.type||record.itemType||record.kind))}${item?.year||record.year?` · ${esc(item?.year||record.year)}`:""}</p></div></div><dl class="detail-fields">${fields.map(([label,value])=>`<dt>${esc(label)}</dt><dd>${esc(value)}</dd>`).join("")}</dl><p class="detail-note">${available?"信息来自当前媒体库。此处为只读预览，不修改媒体内容。":"当前媒体信息不可用，以上显示的是历史记录快照。"}${record.partial?" 本条缺少开始事件，会话时长不完整。":""}</p><button id="detailHistory" class="button detail-history" type="button">查看这部内容的记录</button>`;
}
async function openDetail(id){
  const record=state.rows.get(id);if(!record)return;state.detailRequest?.abort();const controller=new AbortController();state.detailRequest=controller;
  $("detailContent").innerHTML=detailMarkup(record,null,false);if(!$("detail").open)$("detail").showModal();images($("detail"));
  let title=record.itemName||record.title||"";
  function bindHistory(){$("detailHistory").onclick=()=>{controller.abort();$("detail").close();state.filters={q:title,type:"",status:"",from:"",to:""};$("filters").reset();$("search").value=title;state.mode="records";state.sort="recent";selectButton("recordMode","mode","records");sortOptions();filterSummary();screen("records");};}
  bindHistory();const timeout=setTimeout(()=>controller.abort(),6000);
  try{const data=await api(`/media/${encodeURIComponent(id)}`,{signal:controller.signal});if(controller.signal.aborted||state.detailRequest!==controller)return;title=data.item?.name||title;$("detailContent").innerHTML=detailMarkup(data.record||record,data.item,data.available);bindHistory();images($("detail"));}catch(error){if(state.detailRequest===controller&&!controller.signal.aborted)toast("媒体信息暂不可用，显示历史快照");}finally{clearTimeout(timeout);}
}
function bind(){
  $("refresh").onclick=load;$("retry").onclick=load;$("theme").onclick=()=>window.HistoryTheme.toggle();window.HistoryTheme.sync();
  $("overviewTab").onclick=()=>screen("overview");$("recordsTab").onclick=()=>screen("records");$("allRecords").onclick=()=>screen("records");
  $("user").onchange=()=>{state.user=$("user").value;state.page=1;$("manage").checked=false;load();};
  $("range").onclick=event=>{const button=event.target.closest("[data-days]");if(!button)return;state.days=Number(button.dataset.days);selectButton("range","days",state.days);load();};
  $("chart").onclick=event=>{const button=event.target.closest("[data-date]");if(button){state.trendDate=button.dataset.date;renderTrend();bars();}};
  $("trendDate").onchange=()=>{state.trendDate=$("trendDate").value;renderTrend();bars();};
  $("ranking").onclick=event=>{const button=event.target.closest("[data-user]");if(button){state.user=button.dataset.user;load();}};
  document.addEventListener("click",event=>{const detail=event.target.closest("[data-detail]"),deletion=event.target.closest("[data-delete]");if(detail)openDetail(detail.dataset.detail);if(deletion)deleteRecord(deletion.dataset.delete);});
  $("recordMode").onclick=event=>{const button=event.target.closest("[data-mode]");if(!button)return;state.mode=button.dataset.mode;state.sort="recent";state.page=1;selectButton("recordMode","mode",state.mode);sortOptions();load();};
  $("filterToggle").onclick=()=>{const show=$("filters").hidden;$("filters").hidden=!show;$("filterToggle").setAttribute("aria-expanded",String(show));if(show)$("search").focus();};
  $("filters").onsubmit=event=>{event.preventDefault();if($("from").value&&$("to").value&&$("from").value>$("to").value){toast("开始日期不能晚于结束日期");return;}state.filters={q:$("search").value.trim(),type:$("kind").value,status:$("status").value,from:$("from").value,to:$("to").value};state.page=1;filterSummary();load();};
  $("reset").onclick=()=>{$("filters").reset();state.filters={q:"",type:"",status:"",from:"",to:""};state.page=1;filterSummary();load();};
  $("sort").onchange=()=>{state.sort=$("sort").value;state.page=1;load();};$("manage").onchange=load;$("clear").onclick=clearRecords;
  $("previous").onclick=()=>{if(state.page>1){state.page--;load();}};$("next").onclick=()=>{if(state.page<state.pages){state.page++;load();}};
  $("closeDetail").onclick=()=>{$("detail").close();state.detailRequest?.abort();};$("detail").addEventListener("close",()=>state.detailRequest?.abort());
  document.addEventListener("visibilitychange",()=>{if(!document.hidden&&$("autoRefresh").checked)load();});
  window.addEventListener("pagehide",()=>{requests.cancel();state.detailRequest?.abort();});
  window.addEventListener("pageshow",event=>{if(event.persisted)load();});
}
function start(){
  if(window.self!==window.top){document.documentElement.dataset.embedded="true";$("standalone").hidden=false;}
  bind();sortOptions();const initial=load();setInterval(()=>{if(!document.hidden&&$("autoRefresh").checked&&!$("detail").open)load();},60000);return initial;
}
start();
