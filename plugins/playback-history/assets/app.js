"use strict";
const BASE = location.pathname.replace(/\/index\.html$/, "").replace(/\/$/, "");
const $ = (id) => document.getElementById(id);
const state = {screen:"board",user:"",users:[],days:30,mode:"records",sort:"recent",page:1,pages:0,total:0,
  filters:{q:"",type:"",status:"",from:"",to:""},mobile:null,trendDate:"",request:null,timer:null,board:null,timeZone:""};
const COLORS = {Movie:"var(--accent)",Episode:"var(--blue)",Other:"var(--purple)"};
const SORTS = {records:[["recent","最近播放"],["oldest","最早播放"],["duration","单次时长"]],items:[["count","播放次数"],["duration","累计时长"],["recent","最近播放"]]};
const esc = (value) => String(value ?? "").replace(/[&<>'"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[c]));
const number = value => Number.isFinite(Number(value))?Math.max(0,Number(value)):0;
const count = value => number(value).toLocaleString("zh-CN");
const hours = value => (number(value)/3600).toLocaleString("zh-CN",{maximumFractionDigits:1});
function duration(value) {const s=number(value),h=Math.floor(s/3600),m=Math.floor(s%3600/60);return h?`${h}小时${m}分`:m?`${m}分钟`:`${Math.floor(s)}秒`;}
function timeOptions(options) {return state.timeZone?{...options,timeZone:state.timeZone}:options;}
function localTime(raw) {return raw?new Date(raw).toLocaleString("zh-CN",timeOptions({month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"})):"—";}
function setTimeZone(name) {
  try{if(typeof name!=="string"||!name)throw new Error("missing time zone");new Intl.DateTimeFormat("zh-CN",{timeZone:name});state.timeZone=name;}catch(_){state.timeZone="";}
  $("timeZoneNote").textContent=state.timeZone?`按 ${state.timeZone} 统计与显示` : "按服务器日期统计 · 时间按浏览器显示";
  updateClock();
}
function userName() {return state.users.find(u=>u.key===state.user)?.name || (state.user?"所选用户":"全部用户");}
function setConnection(kind,text) {$("connectionStatus").className=`connection ${kind}`;$("connectionStatus").querySelector("span").textContent=text;}
function csrfToken() {
  const match=document.cookie.match(/(?:^|;\s*)nova_csrf=([^;]*)/);
  if(!match)return "";
  try{return decodeURIComponent(match[1]);}catch(_){return "";}
}
async function api(path,options={}) {
  const headers=new Headers(options.headers||{});headers.set("Accept","application/json");if(options.body)headers.set("Content-Type","application/json");
  if(!["GET","HEAD","OPTIONS"].includes((options.method||"GET").toUpperCase())) {
    const token=csrfToken();if(token)headers.set("X-Nova-CSRF",token);
  }
  const response=await fetch(BASE+path,{...options,headers,credentials:"same-origin"});
  if(!response.ok) {let message=`请求失败 (${response.status})`;try{message=(await response.json()).error||message;}catch(_){}throw new Error(message);}
  return response.json();
}
function historyParams() {
  const p=new URLSearchParams({page:String(state.page),pageSize:"12",sort:state.sort});
  if(state.user)p.set("user",state.user);
  for(const [key,value] of Object.entries(state.filters))if(value)p.set(key,value);
  return p;
}
async function loadData(showSpinner=true) {
  state.request?.abort();const request=new AbortController();state.request=request;
  const screen=state.screen;const timeout=setTimeout(()=>request.abort(),20000);
  if(showSpinner)$("loading").classList.remove("hidden");
  $("errorBanner").classList.add("hidden");
  const p=new URLSearchParams({days:String(state.days)});if(state.user)p.set("user",state.user);
  const path=screen==="board"?"/dashboard?"+p:"/"+(state.mode==="items"?"items":"records")+"?"+historyParams();
  try {
    const results=await Promise.allSettled([api(path,{signal:request.signal}),api("/users",{signal:request.signal})]);
    if(state.request!==request)return;
    if(results[0].status==="rejected")throw results[0].reason;
    if(results[1].status==="fulfilled") {state.users=results[1].value.users||[];renderUserSelect();}
    const data=results[0].value;
    if(screen==="board") {state.board=data;setTimeZone(data.timeZone);renderDashboard(data);}
    else renderHistory(data);
    if(results[1].status==="rejected") {$("errorBanner").textContent="用户列表暂时读取失败，已保留当前选择。";$("errorBanner").classList.remove("hidden");}
    $("setupNotice").classList.toggle("hidden",screen!=="board"||data.stats?.totalPlayCount>0||state.user!==""||state.users.some(u=>u.playCount>0));
    $("lastUpdated").textContent="最近同步 "+new Date().toLocaleTimeString("zh-CN",timeOptions({}));
    setConnection("online","数据已同步");
  } catch(error) {
    if(state.request!==request)return;
    $("errorBanner").textContent=(error.name==="AbortError"?"同步超时":error.message)+"；保留上次成功数据，下一次自动更新时重试。";
    $("errorBanner").classList.remove("hidden");setConnection("offline","同步中断");
  } finally {clearTimeout(timeout);if(state.request===request){state.request=null;$("loading").classList.add("hidden");}}
}
function renderUserSelect() {
  $("userSelect").innerHTML='<option value="">全部用户</option>'+state.users.map(u=>`<option value="${esc(u.key)}">${esc(u.name||"未知用户")}${u.deleted?"（已删除）":""}</option>`).join("");
  if(state.user&&!state.users.some(u=>u.key===state.user))$("userSelect").insertAdjacentHTML("beforeend",`<option value="${esc(state.user)}">所选用户</option>`);
  $("userSelect").value=state.user;
}
function renderDashboard(data) {
  const stats=data.stats||{},today=data.today||{};
  $("scopeCaption").textContent=`${userName()} · 最近 ${data.days} 个自然日 · ${data.from} — ${data.to}`;
  $("statPlays").textContent=count(stats.totalPlayCount);$("todayPlays").textContent=`今日 ${count(today.totalPlayCount)} 次播放`;
  $("statHours").textContent=hours(stats.totalWatchSeconds);$("todayHours").textContent=`今日 ${hours(today.totalWatchSeconds)} 小时 · 含暂停`;
  $("statItems").textContent=count(stats.uniqueItems);$("completedNote").textContent=`已看完 ${count(stats.completedCount)} 次`;
  $("statActive").textContent=count(data.activeTotal);$("sessionCount").textContent=count(data.activeTotal);
  renderTrend(data.daily||[]);renderRanking(data.users||[]);renderSessions(data.active||[],data.at);renderPopular(data.popular||[]);
  renderTypes(data.types||[]);renderHours(data.hours||[]);renderRecent(data.recent||[]);applyBars();bindPosters();
}
function empty(message,detail="") {return `<div class="mini-empty"><div><strong>${esc(message)}</strong>${esc(detail)}</div></div>`;}
function renderTrend(days) {
  const width=Math.max(260,$("trendChart").clientWidth||900),height=Math.max(130,$("trendChart").clientHeight||190),left=38,right=10,top=12,bottom=27,plotW=width-left-right,plotH=height-top-bottom;
  const values=days.map(d=>number(d.watchSeconds)/3600),max=Math.max(1,...values),ceiling=Math.ceil(max*1.15*10)/10;
  const x=i=>left+i*plotW/Math.max(1,days.length-1),y=v=>top+plotH-(v/ceiling*plotH);
  const points=values.map((v,i)=>[x(i),y(v)]),line=points.map(([a,b],i)=>`${i?"L":"M"}${a.toFixed(2)},${b.toFixed(2)}`).join(" ");
  const area=points.length?`${line} L${x(points.length-1)},${top+plotH} L${left},${top+plotH} Z`:"";
  let grid="";for(let i=0;i<=3;i++){const value=ceiling*i/3,yy=y(value);grid+=`<line class="chart-grid" x1="${left}" y1="${yy}" x2="${width-right}" y2="${yy}"/><text class="chart-label" x="${left-8}" y="${yy+3}" text-anchor="end">${value.toFixed(value<1?1:0)}</text>`;}
  const step=Math.max(1,Math.ceil((days.length-1)/(width<500?3:6)));let labels="";days.forEach((d,i)=>{if((i%step===0&&days.length-1-i>=step*.55)||i===days.length-1)labels+=`<text class="chart-label" x="${x(i)}" y="${height-5}" text-anchor="${i===0?"start":i===days.length-1?"end":"middle"}">${esc(d.date.slice(5))}</text>`;});
  const dots=days.map((d,i)=>`<circle class="chart-point" data-trend-date="${esc(d.date)}" cx="${x(i)}" cy="${y(values[i])}" r="6"><title>${esc(d.date)} · ${duration(d.watchSeconds)} · ${count(d.playCount)} 次</title></circle>`).join("");
  $("trendChart").innerHTML=`<svg viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" aria-hidden="true"><defs><linearGradient id="trendGradient" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="var(--accent)" stop-opacity=".22"/><stop offset="100%" stop-color="var(--accent)" stop-opacity="0"/></linearGradient></defs>${grid}<path class="trend-area" d="${area}"/><path class="trend-line" d="${line}"/>${dots}${labels}</svg>`;
  syncTrendDetail(days);
  const peak=days.reduce((best,d)=>number(d.watchSeconds)>number(best?.watchSeconds)?d:best,null);
  $("trendSummary").textContent=peak?.watchSeconds?`峰值 ${peak.date.slice(5)} · ${duration(peak.watchSeconds)}`:"该时间范围暂无播放数据";
  $("trendChart").setAttribute("aria-label",days.map(d=>`${d.date} ${duration(d.watchSeconds)}`).join("；"));
}
function syncTrendDetail(days) {
  const picker = $("trendDate");
  picker.disabled = days.length === 0;
  picker.min = days[0]?.date || "";
  picker.max = days[days.length - 1]?.date || "";
  const selected = days.find(day => day.date === state.trendDate) || days[days.length - 1];
  state.trendDate = selected?.date || "";
  picker.value = state.trendDate;
  document.querySelectorAll("[data-trend-date]").forEach(point => point.classList.toggle("selected", point.dataset.trendDate === state.trendDate));
  $("trendDetail").textContent = selected ? `${count(selected.playCount)} 次播放 · ${duration(selected.watchSeconds)}` : "暂无数据";
}
function selectTrendDay(clientX) {
  const days = state.board?.daily || [];
  if (!days.length) return;
  const bounds = $("trendChart").getBoundingClientRect();
  const plotWidth = Math.max(1, bounds.width - 48);
  const index = Math.max(0, Math.min(days.length - 1, Math.round((clientX - bounds.left - 38) / plotWidth * (days.length - 1))));
  state.trendDate = days[index].date;
  syncTrendDetail(days);
}
function isMobile() {
  return window.matchMedia ? window.matchMedia("(max-width: 760px)").matches : window.innerWidth <= 760;
}
function syncMobileLayout() {
  const mobile = isMobile();
  if (state.mobile !== mobile) $("historyFilters").open = !mobile;
  state.mobile = mobile;
}
function updateFilterCount() {
  const active = Object.values(state.filters).filter(Boolean).length;
  $("filterCount").textContent = active ? `已选 ${active} 项` : "全部记录";
}
function finishFiltering() {
  updateFilterCount();
  if (isMobile()) {
    $("historyFilters").open = false;
    $("historySummary").scrollIntoView?.({block:"start", behavior:"auto"});
  }
  state.page = 1;
  loadData();
}
function renderRanking(users) {
  const max=Math.max(1,...users.map(u=>number(u.totalWatchSeconds)));
  $("userRanking").innerHTML=users.length?users.slice(0,5).map((u,i)=>`<button class="rank-row" type="button" data-user="${esc(u.key)}" title="查看 ${esc(u.name)} 的看板"><span class="rank-position">${String(i+1).padStart(2,"0")}</span><span class="avatar">${esc((u.name||"?").slice(0,1))}</span><span class="rank-info"><strong>${esc(u.name||"未知用户")}</strong><span class="rank-track"><i data-width="${number(u.totalWatchSeconds)/max*100}"></i></span></span><span class="rank-meta">${hours(u.totalWatchSeconds)} 小时<small>${count(u.playCount)} 次播放</small></span></button>`).join(""):empty("暂无用户观看记录","播放后这里会显示用户排行");
}
function progress(rec) {return rec.runtimeTicks>0?Math.max(0,Math.min(100,number(rec.positionTicks)*100/rec.runtimeTicks)):0;}
function progressText(rec) {return rec.runtimeTicks>0?`${progress(rec).toFixed(0)}%`:rec.positionTicks>0?`位置 ${duration(rec.positionTicks/1e7)}`:"进度未知";}
function cleanMediaTitle(title) {
  if(!title||!title.includes("."))return title||"";
  const parts=title.split(/[.\s]+/).filter(Boolean),tech=/^(?:\d{3,4}p|WEB-?DL|BLURAY|BDRIP|REMUX|H26[45]|x26[45]|HEVC|AVC|AAC|AC3|DDP|HDR|DV|10BIT|HQ)$/i;
  const at=parts.findIndex(p=>tech.test(p));return at<0?title:parts.slice(0,at).join(" ")||title;
}
function displayTitle(rec) {let title=rec.seriesName||"";if(title&&rec.seasonNumber!=null&&rec.episodeNumber!=null)title+=` S${String(rec.seasonNumber).padStart(2,"0")}E${String(rec.episodeNumber).padStart(2,"0")}`;return title?(title+(rec.itemName&&rec.itemName!==rec.seriesName?" · "+cleanMediaTitle(rec.itemName):"")):cleanMediaTitle(rec.itemName)||rec.title||"未知条目";}
function renderSessions(records,at) {
  $("activeList").innerHTML=records.length?records.slice(0,2).map(rec=>{
    const stale=(new Date(at)-new Date(rec.lastEventAt))/60000>15;
    return `<article class="session-card"><div class="session-top"><span class="avatar">${esc((rec.userName||"?").slice(0,1))}</span><span class="session-user"><strong>${esc(rec.userName)}</strong><small>${esc([rec.deviceName,rec.client].filter(Boolean).join(" · ")||"未知设备")}</small></span></div><h3 class="session-title" title="${esc(displayTitle(rec))}">${esc(displayTitle(rec))}</h3><div class="session-meta"><span>${esc(progressText(rec))}</span><span>经过 ${duration(rec.durationSeconds)}</span></div><div class="progress-track"><div class="progress-fill" data-width="${progress(rec)}"></div></div>${stale?'<span class="session-note">超过 15 分钟无事件 · 状态待确认</span>':`<span class="session-note">最后事件 ${localTime(rec.lastEventAt)}</span>`}</article>`;
  }).join(""):empty("暂无未结束会话","新的播放开始后将在这里展示");
}
function posterURLs(rec) {const ids=[rec.itemId,rec.seriesId].filter((id,i,a)=>id&&a.indexOf(id)===i);return ["/emby",""].flatMap(prefix=>ids.map(id=>`${prefix}/Items/${encodeURIComponent(id)}/Images/Primary?maxWidth=180&quality=75`));}
function poster(rec,title,inline=false) {
  const urls = posterURLs(rec);
  const tag = inline ? "span" : "div";
  return `<${tag} class="poster"><span>${esc((title||"?").slice(0,1))}</span>${urls.length?`<img src="${esc(urls[0])}" data-fallbacks="${esc(JSON.stringify(urls.slice(1)))}" alt="" loading="lazy">`:""}</${tag}>`;
}
function renderPopular(items) {$("popularList").innerHTML=items.length?items.map((item,i)=>`<article class="popular-row">${poster(item,item.title)}<div class="popular-info"><strong title="${esc(item.title)}">${esc(item.title)}</strong><small>${item.itemType==="Movie"?"电影":item.itemType==="Episode"?"剧集":"视频"} · ${duration(item.totalWatchSeconds)}</small></div><span class="popular-number">${count(item.playCount)}<small>次</small></span></article>`).join(""):empty("暂无热门内容","按所选范围的播放次数排序");}
function renderTypes(types) {
  const total=types.reduce((n,t)=>n+number(t.count),0),r=43,c=2*Math.PI*r;let offset=0;
  const segments=types.map(t=>{const length=number(t.count)/Math.max(1,total)*c,seg=`<circle class="donut-segment" cx="60" cy="60" r="${r}" stroke="${COLORS[t.type]||COLORS.Other}" stroke-dasharray="${length} ${c-length}" stroke-dashoffset="${-offset}"/>`;offset+=length;return seg;}).join("");
  const legend=["Movie","Episode","Other"].map(type=>{const n=number(types.find(t=>t.type===type)?.count),label=type==="Movie"?"电影":type==="Episode"?"剧集":"其他";return `<div class="type-row"><i class="type-${type.toLowerCase()}"></i><span>${label}</span><strong>${count(n)}</strong><small>${total?Math.round(n/total*100):0}%</small></div>`;}).join("");
  $("typeChart").innerHTML=`<svg class="donut" viewBox="0 0 120 120" role="img" aria-label="内容分布，共 ${total} 次播放"><circle class="donut-bg" cx="60" cy="60" r="${r}"/>${segments}<text class="donut-total" x="60" y="60" text-anchor="middle">${count(total)}</text><text class="donut-caption" x="60" y="78" text-anchor="middle">总播放次数</text></svg><div class="type-legend">${legend}</div>`;
}
function renderHours(rows) {const max=Math.max(1,...rows.map(h=>number(h.playCount)));$("hourChart").innerHTML=rows.map(h=>`<div class="hour-bar ${h.playCount===max?"peak":""}" title="${h.hour}:00 · ${count(h.playCount)} 次"><i data-height="${number(h.playCount)/max*100}"></i></div>`).join("");$("hourChart").setAttribute("aria-label",rows.map(h=>`${h.hour}点${h.playCount}次`).join("；"));}
function renderRecent(records) {
  $("recentList").innerHTML = records.length ? records.map(rec => {
    const title = displayTitle(rec);
    const query = rec.itemName || rec.seriesName || "";
    return `<button class="recent-card" type="button" data-history-query="${esc(query)}" aria-label="查看 ${esc(title)} 的播放记录">${poster(rec,rec.itemName,true)}<span class="recent-info"><strong>${esc(title)}</strong><small>${esc(rec.userName)} · ${localTime(rec.startedAt)}</small></span><span class="row-chevron" aria-hidden="true">›</span></button>`;
  }).join("") : empty("还没有播放记录", "播放视频后，你的观看足迹会出现在这里");
}
function openRecent(query) {
  $("filterForm").reset();
  state.mode = "records";
  state.sort = "recent";
  state.filters = {q:query,type:"",status:"",from:"",to:""};
  $("searchInput").value = query;
  document.querySelectorAll("[data-mode]").forEach(el => {
    el.classList.toggle("selected",el.dataset.mode==="records");
    el.setAttribute("aria-pressed",String(el.dataset.mode==="records"));
  });
  updateFilterCount();
  setScreen("history");
}
function applyBars() {document.querySelectorAll("[data-width]").forEach(el=>{el.style.width=Math.min(100,number(el.dataset.width))+"%";});document.querySelectorAll("[data-height]").forEach(el=>{el.style.height=Math.min(100,number(el.dataset.height))+"%";});}
function bindPosters() {
  document.querySelectorAll(".poster img").forEach(img=>{
    const placeholder=img.closest(".poster").querySelector("span");let fallbacks=[];try{fallbacks=JSON.parse(img.dataset.fallbacks||"[]");}catch(_){}
    const failed=()=>{if(fallbacks.length){img.src=fallbacks.shift();return;}img.remove();placeholder.hidden=false;};
    img.addEventListener("load",()=>{placeholder.hidden=true;});img.addEventListener("error",failed);
    if(img.complete){if(img.naturalWidth>0)placeholder.hidden=true;else failed();}
  });
}
function renderHistory(data) {
  state.total=number(data.total);state.page=number(data.page)||1;state.pages=number(data.pages);
  $("scopeCaption").textContent=`${userName()} · 全部历史 · 可按关键词、类型、状态与日期筛选`;
  $("historyTitle").textContent=state.mode==="items"?"内容汇总":"播放明细";
  $("historySummary").textContent=`共 ${count(state.total)} ${state.mode==="items"?"项内容":"条记录"} · 累计 ${duration(data.stats?.totalWatchSeconds)} · 已看完 ${count(data.stats?.completedCount)} 次`;
  const rows=data.records||[];$("emptyState").classList.toggle("hidden",rows.length>0);
  const manage=$("manageMode").checked;
  $("clearButton").classList.toggle("hidden",!manage);$("clearButton").textContent=state.user?`清空 ${userName()} 的全部记录`:"清空所有用户全部记录";
  $("recordList").innerHTML=rows.map(rec=>{
    const item=state.mode==="items",title=item?rec.title:displayTitle(rec),when=item?rec.lastPlayedAt:rec.startedAt;
    return `<article class="record">${poster(rec,item?rec.title:rec.itemName)}<div class="record-main"><h3 class="record-title">${esc(title)}${!item&&!rec.endedAt?'<span class="badge waiting">未结束</span>':!item&&rec.completed?'<span class="badge">已看完</span>':""}</h3><div class="record-sub">${item?`播放 ${count(rec.playCount)} 次 · 看完 ${count(rec.completedCount)} 次`:esc(rec.userName||"未知用户")}</div></div><div class="record-device"><div class="record-sub">${item?(rec.itemType==="Movie"?"电影":"剧集"):esc(rec.deviceName||"未知设备")}</div><div class="record-sub">${item?"累计统计":esc(rec.client||"")}</div></div><div class="record-time record-sub">${localTime(when)}</div><div class="record-progress"><span class="progress-caption">${item?"累计会话时长":esc(progressText(rec))}</span>${item?"":`<div class="progress-track"><div class="progress-fill" data-width="${progress(rec)}"></div></div>`}<div class="record-sub">${duration(item?rec.totalWatchSeconds:rec.durationSeconds)}</div></div>${manage&&!item?`<button class="delete-button" type="button" data-delete="${esc(rec.id)}">删除</button>`:""}</article>`;
  }).join("");
  $("pageNumber").textContent=`${state.page} / ${Math.max(1,state.pages)}`;$("prevPage").disabled=state.page<=1;$("nextPage").disabled=state.page>=state.pages;
  applyBars();bindPosters();
}
function setScreen(screen) {
  if (!["board","history"].includes(screen)) return;
  state.screen=screen;state.page=1;
  $("boardView").classList.toggle("hidden",screen!=="board");$("historyView").classList.toggle("hidden",screen!=="history");
  for(const [id,which] of [["boardTab","board"],["historyTab","history"]]) {$(id).classList.toggle("selected",screen===which);$(id).setAttribute("aria-pressed",String(screen===which));}
  $("windowChips").classList.toggle("hidden",screen!=="board");$("pageTitle").innerHTML=screen==="board"?'<span class="desktop-title">播放概况</span><span class="mobile-title">概况</span>':'<span class="desktop-title">播放记录</span><span class="mobile-title">记录</span>';
  $("manageMode").checked=false;syncSort();
  if(isMobile()) window.scrollTo?.({top:0,behavior:"auto"});
  loadData();
}
function syncSort() {$("sortSelect").innerHTML=SORTS[state.mode].map(([value,label])=>`<option value="${value}">${label}</option>`).join("");$("sortSelect").value=state.sort;}
let toastTimer;
function toast(text) {clearTimeout(toastTimer);$("toast").textContent=text;$("toast").classList.remove("hidden");toastTimer=setTimeout(()=>$("toast").classList.add("hidden"),4000);}
async function deleteRecord(id) {
  if(!$("manageMode").checked||!confirm("确定永久删除这条播放记录？"))return;
  try{await api(`/records/${encodeURIComponent(id)}`,{method:"DELETE"});toast("记录已删除");await loadData(false);}catch(error){toast(error.message);}
}
async function clearRecords() {
  const user=state.user,scope=user?`${userName()} 的全部记录`:"所有用户的全部记录";
  if(!$("manageMode").checked||!confirm(`确定永久清空${scope}？\n此操作不受列表日期或关键词筛选限制，无法撤销。`))return;
  try{const result=await api("/clear",{method:"POST",body:JSON.stringify({userKey:user,confirm:true})});toast(`已清理 ${result.removed} 条记录`);state.page=1;await loadData(false);}catch(error){toast(error.message);}
}
function updateAutoRefresh() {clearInterval(state.timer);state.timer=null;if($("autoRefresh").checked)state.timer=setInterval(()=>{if(!document.hidden)loadData(false);},60000);}
function updateClock() {const now=new Date();$("clockTime").textContent=now.toLocaleTimeString("zh-CN",timeOptions({hour12:false}));$("clockDate").textContent=now.toLocaleDateString("zh-CN",timeOptions({year:"numeric",month:"2-digit",day:"2-digit",weekday:"short"}));}
function bindEvents() {
  $("recentList").addEventListener("click", event => {
    const card = event.target.closest("[data-history-query]");
    if (card) openRecent(card.dataset.historyQuery);
  });
  $("trendDate").addEventListener("change", event => {
    state.trendDate = event.target.value;
    syncTrendDetail(state.board?.daily || []);
  });
  $("trendChart").addEventListener("click", event => selectTrendDay(event.clientX));
  $("trendChart").addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    const days = state.board?.daily || [];
    if (!days.length) return;
    event.preventDefault();
    const current = Math.max(0, days.findIndex(day => day.date === state.trendDate));
    const index = event.key === "Home" ? 0 : event.key === "End" ? days.length - 1 : Math.max(0, Math.min(days.length - 1, current + (event.key === "ArrowLeft" ? -1 : 1)));
    state.trendDate = days[index].date;
    syncTrendDetail(days);
  });
  window.NovaTheme.sync();
  $("themeButton").addEventListener("click", () => window.NovaTheme.toggle());
  $("boardTab").addEventListener("click",()=>setScreen("board"));$("historyTab").addEventListener("click",()=>setScreen("history"));$("moreHistory").addEventListener("click",()=>setScreen("history"));
  $("userSelect").addEventListener("change",event=>{state.user=event.target.value;state.page=1;loadData();});
  $("userRanking").addEventListener("click",event=>{const row=event.target.closest("[data-user]");if(row){state.user=row.dataset.user;state.page=1;loadData();}});
  $("windowChips").addEventListener("click",event=>{const button=event.target.closest("[data-days]");if(!button)return;state.days=Number(button.dataset.days);document.querySelectorAll("[data-days]").forEach(el=>{const on=Number(el.dataset.days)===state.days;el.classList.toggle("selected",on);el.setAttribute("aria-pressed",String(on));});loadData();});
  $("historyMode").addEventListener("click",event=>{const button=event.target.closest("[data-mode]");if(!button)return;state.mode=button.dataset.mode;state.sort=state.mode==="items"?"count":"recent";state.page=1;document.querySelectorAll("[data-mode]").forEach(el=>{el.classList.toggle("selected",el.dataset.mode===state.mode);el.setAttribute("aria-pressed",String(el.dataset.mode===state.mode));});syncSort();loadData();});
  $("filterForm").addEventListener("submit",event=>{event.preventDefault();const from=$("fromFilter").value,to=$("toFilter").value;if(from&&to&&from>to){toast("开始日期不能晚于结束日期");return;}state.filters={q:$("searchInput").value.trim(),type:$("typeFilter").value,status:$("statusFilter").value,from,to};finishFiltering();});
  $("resetFilters").addEventListener("click",()=>{$("filterForm").reset();state.filters={q:"",type:"",status:"",from:"",to:""};finishFiltering();});
  $("sortSelect").addEventListener("change",event=>{state.sort=event.target.value;state.page=1;loadData();});
  $("manageMode").addEventListener("change",()=>loadData(false));$("clearButton").addEventListener("click",clearRecords);
  $("recordList").addEventListener("click",event=>{const button=event.target.closest("[data-delete]");if(button)deleteRecord(button.dataset.delete);});
  $("prevPage").addEventListener("click",()=>{if(state.page>1){state.page--;loadData();}});$("nextPage").addEventListener("click",()=>{if(state.page<state.pages){state.page++;loadData();}});
  $("autoRefresh").addEventListener("change",updateAutoRefresh);document.addEventListener("visibilitychange",()=>{if(!document.hidden){updateClock();if($("autoRefresh").checked)loadData(false);}});
  if(!document.documentElement.requestFullscreen||document.fullscreenEnabled===false)$("fullscreenButton").hidden=true;
  $("fullscreenButton").addEventListener("click",async()=>{try{if(document.fullscreenElement)await document.exitFullscreen();else await document.documentElement.requestFullscreen();}catch(_){toast("当前浏览器不支持全屏，可使用浏览器的全屏功能");}});
  document.addEventListener("fullscreenchange",()=>{const label=document.fullscreenElement?"退出全屏":"全屏展示";$("fullscreenButton").setAttribute("aria-label",label);$("fullscreenButton").title=label;});
}
async function init() {bindEvents();syncMobileLayout();updateFilterCount();
  let resizeTimer;window.addEventListener("resize",()=>{syncMobileLayout();clearTimeout(resizeTimer);resizeTimer=setTimeout(()=>{if(state.screen==="board"&&state.board)renderTrend(state.board.daily||[]);},150);});
  syncSort();updateClock();setInterval(()=>{if(!document.hidden)updateClock();},1000);updateAutoRefresh();await loadData();}
init();
