// Node 内置测试，无第三方依赖；不代替真实浏览器布局验收。
import {readFileSync} from "node:fs";
import vm from "node:vm";
import test from "node:test";
import assert from "node:assert/strict";
const html=readFileSync(new URL("./assets/index.html",import.meta.url),"utf8");
const themeSource=readFileSync(new URL("./assets/theme.js",import.meta.url),"utf8");
const source=readFileSync(new URL("./assets/app.js",import.meta.url),"utf8").replace(/init\(\);\s*$/,"");
function page(fetch,pathname="/history",options={}) {
  const elements=new Map(),intervals=new Map(),documentEvents=new Map(),frames=[],observers=[];let intervalID=0;
  for(const match of html.matchAll(/<[^>]+id="([^"]+)"[^>]*>/g)) {
    const classes=new Set((match[0].match(/class="([^"]+)"/)?.[1]||"").split(" "));
    const attrs=new Map(),listeners=new Map(),child={textContent:""};
    elements.set(match[1],{value:"",checked:match[0].includes(" checked"),hidden:false,textContent:"",innerHTML:"",style:{},attrs,listeners,
      classList:{add:x=>classes.add(x),remove:x=>classes.delete(x),contains:x=>classes.has(x),toggle(x,on){const value=on??!classes.has(x);value?classes.add(x):classes.delete(x);return value;}},
      getBoundingClientRect:()=>({left:10,width:352}),setAttribute:(k,v)=>attrs.set(k,v),addEventListener:(k,v)=>listeners.set(k,v),
      querySelector:()=>child,insertAdjacentHTML(_,s){this.innerHTML+=s;},
      reset(){for(const id of ["searchInput","typeFilter","statusFilter","fromFilter","toFilter"])elements.get(id).value="";}
    });
  }
  const saved=new Map(options.theme?[["novaemby.playback-history.theme",options.theme]]:[]);
  const windowEvents=new Map();
  const storage=options.blockStorage?{getItem(){throw new Error("blocked");},setItem(){throw new Error("blocked");}}:{getItem:key=>saved.get(key)||null,setItem:(key,value)=>saved.set(key,value)};
  const window={innerWidth:options.width||1440,matchMedia(){return {matches:this.innerWidth<=760};},scrollTo(){},addEventListener:(key,value)=>windowEvents.set(key,value),localStorage:storage};
  window.self=window;window.top=options.embedded?{}:window;
  window.frameElement=options.frame||null;
  if(options.blockFrame)Object.defineProperty(window,"frameElement",{get(){throw new Error("cross origin");}});
  window.requestAnimationFrame=callback=>frames.push(callback);
  window.removeEventListener=(key,value)=>{if(windowEvents.get(key)===value)windowEvents.delete(key);};
  const meta={attrs:new Map(),setAttribute(key,value){this.attrs.set(key,value);}};
  const document={cookie:options.cookie||"",hidden:false,fullscreenEnabled:options.fullscreenEnabled??true,fullscreenElement:null,documentElement:{dataset:{theme:"light"},requestFullscreen:async()=>{}},querySelector:()=>meta,
    getElementById:id=>elements.get(id)||null,querySelectorAll:()=>[],addEventListener:(k,v)=>documentEvents.set(k,v)};
  document.body={height:800,getBoundingClientRect(){return {height:this.height};}};
  document.removeEventListener=(key,value)=>{if(documentEvents.get(key)===value)documentEvents.delete(key);};
  class ResizeObserver {constructor(callback){this.callback=callback;observers.push(this);}observe(target){this.target=target;}disconnect(){this.disconnected=true;}}
  const context=vm.createContext({document,window,ResizeObserver:options.noObserver?undefined:ResizeObserver,location:{pathname},fetch,Headers,URLSearchParams,AbortController,setTimeout,clearTimeout,
    setInterval(fn,ms){const id=++intervalID;intervals.set(id,{fn,ms});return id;},clearInterval:id=>intervals.delete(id),confirm:()=>options.confirm??false});
  vm.runInContext(themeSource,context);
  vm.runInContext(source,context);
  return {context,elements,document,intervals,saved,meta,windowEvents,observers,flushFrames(){for(const callback of frames.splice(0))callback();},run:code=>vm.runInContext(code,context)};
}
const response=data=>({ok:true,json:async()=>data});
const board=(plays=4)=>({at:"2026-11-08T12:00:00Z",from:"2026-10-10",to:"2026-11-08",days:30,stats:{totalPlayCount:plays,totalWatchSeconds:7200,uniqueItems:2,completedCount:1},today:{totalPlayCount:1,totalWatchSeconds:100},activeTotal:0,active:[],recent:[],users:[],popular:[],daily:[{date:"2026-11-08",watchSeconds:7200,playCount:plays}],hours:Array.from({length:24},(_,hour)=>({hour,playCount:hour===12?plays:0})),types:[{type:"Movie",count:plays}]});
const users={users:[{key:"name:alice",name:"Alice",playCount:4}]};

test("页面完整初始化、图表和指标渲染、自动更新默认开启",async()=>{
  const urls=[];const p=page(async url=>{urls.push(url);return response(url.includes("/dashboard")?board():users);});
  await p.run("init()");
  assert.equal(p.elements.get("statPlays").textContent,"4");
  assert.match(p.elements.get("trendChart").innerHTML,/<svg/);
  assert.match(p.elements.get("typeChart").innerHTML,/donut-segment/);
  assert.equal((p.elements.get("hourChart").innerHTML.match(/class="hour-bar/g)||[]).length,24);
  assert.equal(p.elements.get("loading").classList.contains("hidden"),true);
  assert.ok([...p.intervals.values()].some(t=>t.ms===60000));
  assert.deepEqual(urls,["/history/dashboard?days=30","/history/users"]);
});

test("快速切换用户，忽略迟到的旧响应",async()=>{
  const pending=[];const p=page((url,options)=>new Promise(resolve=>pending.push({url,options,resolve})));
  const old=p.run("loadData()");const latest=p.run('state.user="name:alice";loadData()');
  assert.equal(pending[0].options.signal.aborted,true);assert.match(pending[2].url,/user=name%3Aalice/);
  pending[2].resolve(response(board(9)));pending[3].resolve(response(users));await latest;
  pending[0].resolve(response(board(1)));pending[1].resolve(response(users));await old;
  assert.equal(p.elements.get("statPlays").textContent,"9");assert.match(p.elements.get("scopeCaption").textContent,/Alice/);
});

test("明细独立分页、继承用户筛选，统计使用服务端全量结果",async()=>{
  const urls=[];const p=page(async url=>{urls.push(url);return response(url.endsWith("/users")?users:{records:[],total:100,page:2,pages:9,stats:{totalWatchSeconds:3600,completedCount:50}});});
  await p.run('state.screen="history";state.user="name:alice";state.page=2;state.filters.q="电影";loadData()');
  assert.match(urls[0],/\/records\?/);assert.match(urls[0],/page=2/);assert.match(urls[0],/user=name%3Aalice/);assert.ok(!urls[0].includes("days="));
  assert.match(p.elements.get("historySummary").textContent,/100 条记录/);assert.match(p.elements.get("historySummary").textContent,/50 次/);
  assert.equal(p.elements.get("pageNumber").textContent,"2 / 9");assert.ok(p.elements.get("clearButton").classList.contains("hidden"));
});

test("同步失败保留旧数据，用户列表失败不阻断看板",async()=>{
  let fail=false;const p=page(async url=>{if(fail)throw new Error("网络中断");if(url.endsWith("/users"))throw new Error("用户列表故障");return response(board(7));});
  await p.run("loadData()");assert.equal(p.elements.get("statPlays").textContent,"7");assert.match(p.elements.get("errorBanner").textContent,/用户列表/);
  fail=true;await p.run("loadData()");assert.equal(p.elements.get("statPlays").textContent,"7");assert.match(p.elements.get("errorBanner").textContent,/保留上次/);
});

test("用户和片名文本转义；管理关闭时不能发删除请求",async()=>{
  const urls=[];const data=board();data.recent=[{itemName:'<img src=x onerror=alert(1)>',userName:'<script>x</script>',startedAt:data.at}];
  const p=page(async url=>{urls.push(url);return response(url.endsWith("/users")?users:data);});
  await p.run("loadData()");const rendered=p.elements.get("recentList").innerHTML;
  assert.ok(rendered.includes("&lt;img"));assert.ok(!rendered.includes("<script>"));
  const n=urls.length;await p.run('deleteRecord("1")');await p.run("clearRecords()");assert.equal(urls.length,n);
});

test("自动刷新跳过后台标签页，支持 /emby/history 前缀",async()=>{
  const urls=[];const p=page(async url=>{urls.push(url);return response(url.endsWith("/users")?users:board());},"/emby/history/");
  await p.run("init()");assert.match(urls[0],/^\/emby\/history\/dashboard/);
  const timer=[...p.intervals.values()].find(t=>t.ms===60000),n=urls.length;
  p.document.hidden=true;timer.fn();assert.equal(urls.length,n);
  p.document.hidden=false;timer.fn();await new Promise(resolve=>setTimeout(resolve,0));assert.equal(urls.length,n+2);
});


test("首次默认亮色，右上角可切换并在下次打开恢复选择",()=>{
  const p=page(async()=>response(board()));
  assert.equal(p.document.documentElement.dataset.theme,"light");
  assert.equal(p.meta.attrs.get("content"),"#f4f4f5");
  p.run("bindEvents()");
  p.elements.get("themeButton").listeners.get("click")();
  assert.equal(p.document.documentElement.dataset.theme,"dark");
  assert.equal(p.elements.get("themeButton").attrs.get("aria-label"),"切换到亮色主题");
  assert.equal(p.elements.get("themeButton").attrs.get("aria-pressed"),"true");
  assert.equal(p.meta.attrs.get("content"),"#18181b");
  const restored=page(async()=>response(board()),"/history",{theme:p.saved.get("novaemby.playback-history.theme")});
  assert.equal(restored.document.documentElement.dataset.theme,"dark");
  restored.run("window.NovaTheme.toggle()");
  assert.equal(restored.document.documentElement.dataset.theme,"light");
  assert.equal(restored.saved.get("novaemby.playback-history.theme"),"light");
});

test("存储不可用或偏好无效时默认亮色，仍能切换",()=>{
  const p=page(async()=>response(board()),"/history",{blockStorage:true});
  assert.equal(p.document.documentElement.dataset.theme,"light");
  assert.doesNotThrow(()=>p.run("window.NovaTheme.toggle()"));
  assert.equal(p.document.documentElement.dataset.theme,"dark");
  const invalid=page(async()=>response(board()),"/history",{theme:"unsupported"});
  assert.equal(invalid.document.documentElement.dataset.theme,"light");
  invalid.windowEvents.get("storage")({key:"novaemby.playback-history.theme",newValue:"dark"});
  assert.equal(invalid.document.documentElement.dataset.theme,"dark");
  invalid.windowEvents.get("storage")({key:null,newValue:null});
  assert.equal(invalid.document.documentElement.dataset.theme,"light");
});


test("手机筛选默认收起，提交后更新条件并收起，旋转宽度切换保留正确状态",async()=>{
  const urls=[];const p=page(async url=>{urls.push(url);return response(url.endsWith("/users")?users:url.includes("/records")?{records:[],total:0,page:1,pages:0,stats:{}}:board());},"/history",{width:390});
  await p.run("init()");assert.equal(p.elements.get("historyFilters").open,false);
  await p.run('state.screen="history";loadData()');
  p.elements.get("historyFilters").open=true;
  p.elements.get("searchInput").value="手机端影片";
  p.elements.get("fromFilter").value="2026-11-01";
  p.elements.get("filterForm").listeners.get("submit")({preventDefault(){}});
  await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(p.elements.get("historyFilters").open,false);
  assert.equal(p.elements.get("filterCount").textContent,"已选 2 项");
  assert.ok(urls.some(url=>url.includes("from=2026-11-01")&&url.includes("/records")));
  p.elements.get("historyFilters").open=true;p.run("syncMobileLayout()");assert.equal(p.elements.get("historyFilters").open,true);
  p.run("window.innerWidth=1200;syncMobileLayout()");assert.equal(p.elements.get("historyFilters").open,true);
  p.run("window.innerWidth=390;syncMobileLayout()");assert.equal(p.elements.get("historyFilters").open,false);
});

test("手机趋势支持日期选择、点按和键盘，无需额外请求",async()=>{
  const data=board();data.daily=[{date:"2026-11-06",watchSeconds:60,playCount:1},{date:"2026-11-07",watchSeconds:120,playCount:2},{date:"2026-11-08",watchSeconds:180,playCount:3}];
  let calls=0;const p=page(async url=>{calls++;return response(url.endsWith("/users")?users:data);},"/history",{width:390});
  await p.run("init()");const count=calls;
  assert.equal(p.elements.get("trendDate").value,"2026-11-08");
  p.elements.get("trendDate").listeners.get("change")({target:{value:"2026-11-07"}});
  assert.match(p.elements.get("trendDetail").textContent,/2 次播放/);
  p.elements.get("trendChart").listeners.get("click")({clientX:48});assert.equal(p.elements.get("trendDate").value,"2026-11-06");
  p.elements.get("trendChart").listeners.get("keydown")({key:"End",preventDefault(){}});assert.equal(p.elements.get("trendDate").value,"2026-11-08");
  p.elements.get("trendChart").listeners.get("keydown")({key:"ArrowLeft",preventDefault(){}});assert.equal(p.elements.get("trendDate").value,"2026-11-07");
  p.run("renderTrend(state.board.daily)");assert.equal(p.elements.get("trendDate").value,"2026-11-07");
  p.run('syncTrendDetail([{date:"2026-11-08",watchSeconds:180,playCount:3}])');assert.equal(p.elements.get("trendDate").value,"2026-11-08");
  assert.equal(calls,count);
});


test("最近播放卡片进入该内容记录，清除旧筛选并保持管理关闭",async()=>{
  const requests=[];
  const p=page(async url=>{requests.push(url);return response(url.endsWith("/users")?users:url.includes("/records")?{records:[],total:0,page:1,pages:0,stats:{}}:board());},"/history",{width:390});
  await p.run("init()");
  p.run('state.mode="items";state.filters.status="completed"');
  p.elements.get("manageMode").checked=true;
  p.elements.get("recentList").listeners.get("click")({target:{closest:()=>({dataset:{historyQuery:"测试影片"}})}});
  await new Promise(resolve=>setTimeout(resolve,0));
  assert.equal(p.run("state.screen"),"history");
  assert.equal(p.run("state.mode"),"records");
  assert.equal(p.run("state.filters.q"),"测试影片");
  assert.equal(p.run("state.filters.status"),"");
  assert.equal(p.elements.get("manageMode").checked,false);
  assert.equal(p.elements.get("filterCount").textContent,"已选 1 项");
  assert.ok(requests.some(url=>url.includes("/records?")&&new URLSearchParams(url.split("?")[1]).get("q")==="测试影片"));
});

test("内嵌看板的删除和清空带宿主 CSRF，读取请求沿用同源登录",async()=>{
  const requests=[];
  const p=page(async(url,options)=>{
    requests.push({url,options});
    if(options.method==="DELETE")return response({deleted:true});
    if(options.method==="POST")return response({removed:2});
    return response(url.endsWith("/users")?users:board());
  },"/emby/history/",{cookie:"other=1; nova_csrf=first%2Btoken%3D; session=private",confirm:true});
  await p.run("init()");
  assert.ok(requests.every(r=>r.options.credentials==="same-origin"));
  assert.ok(requests.every(r=>!r.options.headers.has("X-Nova-CSRF")));
  p.elements.get("manageMode").checked=true;
  await p.run('deleteRecord("记录/1")');
  const deletion=requests.find(r=>r.options.method==="DELETE");
  assert.equal(deletion.url,"/emby/history/records/%E8%AE%B0%E5%BD%95%2F1");
  assert.equal(deletion.options.headers.get("X-Nova-CSRF"),"first+token=");
  p.document.cookie="nova_csrf=renewed-token";
  await p.run("clearRecords()");
  const clearing=requests.find(r=>r.options.method==="POST");
  assert.equal(clearing.url,"/emby/history/clear");
  assert.equal(clearing.options.headers.get("X-Nova-CSRF"),"renewed-token");
  assert.equal(clearing.options.headers.get("Content-Type"),"application/json");
  assert.equal(JSON.parse(clearing.options.body).confirm,true);
});

test("直接访问没有 Cookie 或 Cookie 编码损坏时仍能请求接口",async()=>{
  for(const cookie of ["","nova_csrf=%broken","other_nova_csrf=unrelated"]) {
    const requests=[];
    const p=page(async(url,options)=>{requests.push({url,options});return response({removed:0});},"/history",{cookie});
    await p.run('api("/clear",{method:"POST",body:JSON.stringify({confirm:true})})');
    assert.equal(requests.length,1);
    assert.equal(requests[0].url,"/history/clear");
    assert.equal(requests[0].options.headers.has("X-Nova-CSRF"),false);
  }
});

test("内嵌框禁止全屏时隐藏入口，仍可查询看板和切换主题",async()=>{
  const p=page(async url=>response(url.endsWith("/users")?users:board()),"/history",{fullscreenEnabled:false,width:390});
  await p.run("init()");
  assert.equal(p.elements.get("fullscreenButton").hidden,true);
  assert.equal(p.elements.get("statPlays").textContent,"4");
  p.elements.get("themeButton").listeners.get("click")();
  assert.equal(p.document.documentElement.dataset.theme,"dark");
});

test("看板和明细时间按宿主时区显示，非法时区不会中断加载",async()=>{
  const data=board();data.timeZone="Asia/Shanghai";
  data.recent=[{itemName:"跨日影片",userName:"Alice",startedAt:"2026-11-11T16:15:00Z"}];
  const p=page(async url=>response(url.endsWith("/users")?users:data));
  await p.run("init()");
  assert.equal(p.run("state.timeZone"),"Asia/Shanghai");
  assert.match(p.elements.get("timeZoneNote").textContent,/Asia\/Shanghai/);
  const expected=new Date("2026-11-11T16:15:00Z").toLocaleString("zh-CN",{timeZone:"Asia/Shanghai",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"});
  assert.equal(p.run('localTime("2026-11-11T16:15:00Z")'),expected);
  assert.ok(p.elements.get("recentList").innerHTML.includes(expected));
  await p.run('state.screen="history";loadData()');
  assert.equal(p.run("state.timeZone"),"Asia/Shanghai");
  assert.doesNotThrow(()=>p.run('setTimeZone("Invalid/TimeZone")'));
  assert.equal(p.run("state.timeZone"),"");
  assert.match(p.elements.get("timeZoneNote").textContent,/浏览器/);
});

test("持续进度更新恢复会话活跃提示并刷新进度条",async()=>{
  const data=board();
  data.active=[{id:"r1",userName:"Alice",itemName:"测试影片",startedAt:"2026-11-08T10:00:00Z",lastEventAt:"2026-11-08T11:00:00Z",positionTicks:10,runtimeTicks:100,durationSeconds:7200}];
  data.activeTotal=1;
  const p=page(async url=>response(url.endsWith("/users")?users:data));
  await p.run("loadData()");
  assert.match(p.elements.get("activeList").innerHTML,/超过 15 分钟无事件/);
  data.active[0].lastEventAt=data.at;data.active[0].positionTicks=65;
  await p.run("loadData()");
  assert.ok(!p.elements.get("activeList").innerHTML.includes("超过 15 分钟无事件"));
  assert.match(p.elements.get("activeList").innerHTML,/最后事件/);
  assert.match(p.elements.get("activeList").innerHTML,/data-width="65"/);
});

function frameFixture() {
  const values=new Map([["height",["480px",""]],["max-height",["600px",""]],["border",["0",""]]]);
  return {values,scrolled:false,scrollIntoView(){this.scrolled=true;},style:{
    getPropertyValue:name=>values.get(name)?.[0]||"",getPropertyPriority:name=>values.get(name)?.[1]||"",
    setProperty(name,value,priority=""){values.set(name,[value,priority]);},removeProperty:name=>values.delete(name)
  }};
}

test("同源内嵌窗口随内容增高与缩小，离开时恢复原窗口样式",()=>{
  const frame=frameFixture();
  const p=page(async()=>response(board()),"/history",{embedded:true,frame,width:390});
  p.run("setupEmbeddedLayout()");p.flushFrames();
  assert.equal(p.document.documentElement.dataset.embedded,"true");
  assert.equal(p.elements.get("openWindowButton").hidden,false);
  assert.equal(frame.style.getPropertyValue("height"),"800px");
  assert.equal(frame.style.getPropertyPriority("height"),"important");
  assert.equal(frame.style.getPropertyValue("max-height"),"none");
  assert.equal(p.observers[0].target,p.document.body);
  p.document.body.height=1700;p.observers[0].callback();p.flushFrames();
  assert.equal(frame.style.getPropertyValue("height"),"1700px");
  p.document.body.height=460;p.observers[0].callback();p.flushFrames();
  assert.equal(frame.style.getPropertyValue("height"),"460px");
  p.document.fullscreenElement={};p.document.body.height=2000;p.observers[0].callback();p.flushFrames();
  assert.equal(frame.style.getPropertyValue("height"),"460px");
  p.document.fullscreenElement=null;
  p.windowEvents.get("pagehide")();
  assert.equal(p.observers[0].disconnected,true);
  assert.equal(frame.style.getPropertyValue("height"),"480px");
  assert.equal(frame.style.getPropertyValue("max-height"),"600px");
  assert.equal(frame.style.getPropertyValue("border"),"0");
  assert.equal(frame.style.getPropertyValue("width"),"");
});

test("内嵌切换看板/明细后更新高度，浏览器返回缓存页面时重新观察",async()=>{
  const frame=frameFixture();
  const p=page(async url=>response(url.endsWith("/users")?users:url.includes("/records")?{records:[],total:0,page:1,pages:0,stats:{}}:board()),"/emby/history/",{embedded:true,frame,width:390});
  await p.run("init()");p.flushFrames();
  p.document.body.height=1200;
  p.run('setScreen("history")');
  await new Promise(resolve=>setTimeout(resolve,0));p.flushFrames();
  assert.equal(frame.style.getPropertyValue("height"),"1200px");
  assert.equal(frame.scrolled,true);
  p.windowEvents.get("pagehide")();
  p.windowEvents.get("pageshow")({persisted:true});p.flushFrames();
  assert.equal(p.observers.length,2);
  assert.equal(frame.style.getPropertyValue("height"),"1200px");
});

test("直接访问和不能访问父框架时均可正常使用看板",async()=>{
  for(const options of [{},{embedded:true,blockFrame:true},{embedded:true,noObserver:true,frame:frameFixture()}]) {
    const p=page(async url=>response(url.endsWith("/users")?users:board()),"/history",options);
    await p.run("init()");p.flushFrames();
    assert.equal(p.elements.get("statPlays").textContent,"4");
    assert.equal(p.document.documentElement.dataset.embedded,options.embedded?"true":undefined);
    if(options.embedded)assert.equal(p.elements.get("openWindowButton").hidden,false);
  }
});
