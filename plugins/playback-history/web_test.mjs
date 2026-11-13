import test from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import {readFileSync} from "node:fs";
import * as core from "./assets/core.mjs";
const html=readFileSync(new URL("./assets/index.html",import.meta.url),"utf8");
const app=readFileSync(new URL("./assets/app.js",import.meta.url),"utf8");
const theme=readFileSync(new URL("./assets/theme.js",import.meta.url),"utf8");
const ok=data=>({ok:true,status:200,json:async()=>data});
const overview=()=>({timeZone:"Asia/Shanghai",from:"2026-11-01",to:"2026-11-30",days:30,stats:{plays:2,seconds:7200,contents:1,completed:1},today:{plays:1,seconds:3600},openCount:0,active:[],recent:[],daily:[{date:"2026-11-30",plays:2,seconds:7200}],users:[],popular:[],kinds:{Movie:2}});
const bootstrap=()=>({version:"1.0.0",timeZone:"Asia/Shanghai",records:2,users:[{key:"name:alice",name:"Alice"}],unsupportedEvents:0,posterTemplate:""});
function ui(fetch,options={}) {
  const elements=new Map(),windowEvents=new Map(),documentEvents=new Map(),intervals=[];
  const allIDs=[...html.matchAll(/id="([^"]+)"/g)].map(m=>m[1]);allIDs.push("detailHistory");
  for(const id of allIDs){const attrs=new Map(),classes=new Set();elements.set(id,{id,hidden:false,disabled:false,checked:id==="autoRefresh",value:"",innerHTML:"",textContent:"",dataset:{},style:{setProperty(){}},classList:{toggle(name,on){if(on)classes.add(name);else classes.delete(name);},contains:name=>classes.has(name)},setAttribute:(key,value)=>attrs.set(key,value),getAttribute:key=>attrs.get(key),addEventListener(){},querySelectorAll(){return [];},insertAdjacentHTML(_,text){this.innerHTML+=text;},reset(){},focus(){},open:false,showModal(){this.open=true;},close(){this.open=false;}});}
  const meta={setAttribute(name,value){this[name]=value;}};
  const document={hidden:false,cookie:options.cookie||"",documentElement:{dataset:{}},getElementById:id=>elements.get(id),querySelector:()=>meta,querySelectorAll:()=>[],addEventListener:(name,fn)=>documentEvents.set(name,fn)};
  const storage=new Map();if(options.theme)storage.set("novaemby.playback-history.theme",options.theme);
  const window={fetch,localStorage:{getItem:key=>{if(options.blockStorage)throw Error("blocked");return storage.get(key);},setItem:(key,value)=>{if(options.blockStorage)throw Error("blocked");storage.set(key,value);}},addEventListener:(name,fn)=>windowEvents.set(name,fn),confirm:()=>options.confirm??false};window.self=window;window.top=options.embedded?{}:window;
  const context=vm.createContext({...core,esc:core.escapeHTML,num:core.nonnegative,document,window,location:{pathname:options.path||"/history"},Headers,URLSearchParams,AbortController,Intl,setTimeout(fn,ms){const timer=setTimeout(fn,ms);timer.unref();return timer;},clearTimeout,setInterval(fn,ms){intervals.push({fn,ms});}});
  vm.runInContext(theme,context);vm.runInContext(app.replace(/^import[^\n]*\n/,"").replace(/start\(\);\s*$/,""),context);
  return {elements,document,window,windowEvents,documentEvents,storage,intervals,meta,run:code=>vm.runInContext(code,context)};
}

test("HTML escaping and title formatting preserve content without markup injection",()=>{
  assert.equal(core.escapeHTML('<img src=x onerror="x">'),"&lt;img src=x onerror=&quot;x&quot;&gt;");
  assert.equal(core.recordTitle({seriesName:"剧名",seasonNumber:2,episodeNumber:3,itemName:"单集"}),"剧名 S02E03 · 单集");
  assert.equal(core.recordTitle({}),"未知内容");assert.equal(core.progress({positionTicks:120,runtimeTicks:100}),100);
});
test("official compatibility prefixes are preserved in plugin API and sidecar URLs",()=>{
  for(const path of ["/history/","/emby/history/","/mediabrowser/history/"]){const base=core.basePath(path);assert.equal(core.posterSources({id:"a/b"},base)[0],base+"/poster/a%2Fb");}
});
test("poster sources prefer sidecars and configured series images, never guess endpoints",()=>{
  const urls=core.posterSources({id:"r1",itemId:"ep/1",seriesId:"show",itemType:"Episode"},"/emby/history","/verified/images/{id}");
  assert.deepEqual(urls,["/emby/history/poster/r1","/verified/images/show","/verified/images/ep%2F1"]);
  for(const invalid of ["https://outside/{id}","//outside/{id}","/\\outside/{id}"]){assert.deepEqual(core.posterSources({id:"r1",itemId:"1"},"/history",invalid),["/history/poster/r1"]);}
});
test("image fallback terminates and reports missing artwork exactly once",()=>{
  const handlers=new Map();let failures=0;const image={dataset:{sources:JSON.stringify(["/local","/configured"])},addEventListener:(event,fn)=>handlers.set(event,fn)};
  core.nextImage(image,()=>failures++);assert.equal(image.src,"/local");handlers.get("error")();assert.equal(image.src,"/configured");handlers.get("error")();handlers.get("error")();assert.equal(image.hidden,true);assert.equal(failures,1);
});
test("successful image becomes visible without replacing the configured source",()=>{
  const handlers=new Map();const image={dataset:{sources:'["/local"]'},addEventListener:(event,fn)=>handlers.set(event,fn)};core.nextImage(image);handlers.get("load")();assert.equal(image.dataset.loaded,"true");assert.equal(image.src,"/local");
});
test("API uses same-origin Cookie and current CSRF for writes, never extracts a token",async()=>{
  const requests=[];let cookie="nova_csrf=first%2Bvalue; session=private";
  const api=core.createAPI(async(url,options)=>{requests.push({url,options});return ok({});},"/mediabrowser/history",()=>cookie);
  await api("/bootstrap");await api("/clear",{method:"POST",body:{user:"name:alice",confirm:true}});cookie="nova_csrf=renewed";await api("/records/1",{method:"DELETE"});
  assert.equal(requests[0].options.headers.has("X-Nova-CSRF"),false);assert.equal(requests[1].options.headers.get("X-Nova-CSRF"),"first+value");assert.equal(requests[2].options.headers.get("X-Nova-CSRF"),"renewed");assert.ok(requests.every(r=>r.options.credentials==="same-origin"));assert.ok(requests.every(r=>!r.options.headers.has("Authorization")));
});
test("missing or corrupt CSRF does not crash read or legacy unauthenticated requests",async()=>{
  for(const cookie of ["","nova_csrf=%bad","other_nova_csrf=x"]){let headers;const api=core.createAPI(async(_,options)=>{headers=options.headers;return ok({});},"/history",()=>cookie);await api("/clear",{method:"POST",body:{confirm:true}});assert.equal(headers.has("X-Nova-CSRF"),false);}
});
test("invalid JSON and unavailable plugin have actionable failures",async()=>{
  const invalid=core.createAPI(async()=>({ok:true,status:200,json:async()=>{throw Error("bad json");}}),"/history",()=>"");await assert.rejects(invalid("/overview"),/无法读取/);
  for(const [status,message]of [[401,/登录/],[403,/权限/],[503,/激活/]]){const api=core.createAPI(async()=>({ok:false,status,json:async()=>({})}),"/history",()=>"");await assert.rejects(api("/overview"),message);}
});
test("late responses and old timeouts cannot cancel or replace the new scope",()=>{
  const queue=core.latestRequest(),old=queue.begin(),current=queue.begin();assert.equal(old.active(),false);assert.equal(old.signal.aborted,true);old.abort();assert.equal(current.signal.aborted,false);queue.cancel();assert.equal(current.active(),false);assert.equal(current.signal.aborted,true);
});
test("record filters and group mode are serialized without losing user scope",()=>{
  const query=new URLSearchParams(core.queryString({mode:"items",sort:"count",page:2,user:"name:Alice",filters:{q:"中文 / 电影",from:"2026-11-01",to:"",type:"Movie"}}));assert.equal(query.get("mode"),"items");assert.equal(query.get("q"),"中文 / 电影");assert.equal(query.get("user"),"name:Alice");assert.equal(query.has("to"),false);
});
test("host timezone controls cross-midnight display, invalid timezone falls back to UTC",()=>{
  assert.equal(core.validZone("invalid/zone"),"UTC");const value=core.dateTime("2026-11-11T16:00:00Z","Asia/Shanghai");assert.match(value,/11\/12/);assert.equal(core.dateTime("broken","UTC"),"—");
});
test("page initializes new metrics, defaults to light theme and persists theme choice",async()=>{
  const p=ui(async url=>ok(url.includes("/bootstrap")?bootstrap():overview()));await p.run("start()");assert.equal(p.elements.get("plays").textContent,"2");assert.equal(p.elements.get("hours").textContent,"2");assert.equal(p.elements.get("zone").textContent,"时区 Asia/Shanghai");assert.equal(p.document.documentElement.dataset.theme,"light");p.elements.get("theme").onclick();assert.equal(p.document.documentElement.dataset.theme,"dark");assert.equal(p.storage.get("novaemby.playback-history.theme"),"dark");assert.ok(p.intervals.some(i=>i.ms===60000));
});
test("theme storage failure still allows toggling",()=>{
  const p=ui(async()=>ok({}),{blockStorage:true});assert.doesNotThrow(()=>p.run("window.HistoryTheme.toggle()"));assert.equal(p.document.documentElement.dataset.theme,"dark");
});
test("newer scope wins even when an earlier server response arrives last",async()=>{
  const pending=[];const p=ui((url,options)=>new Promise(resolve=>pending.push({url,options,resolve})));const first=p.run("load()");const second=p.run('state.user="name:alice";load()');assert.equal(pending[0].options.signal.aborted,true);pending[2].resolve(ok(bootstrap()));const recent=overview();recent.stats.plays=9;pending[3].resolve(ok(recent));await second;pending[0].resolve(ok(bootstrap()));pending[1].resolve(ok(overview()));await first;assert.equal(p.elements.get("plays").textContent,"9");
});
test("failed refresh preserves successful data with visible error and retry",async()=>{
  let fail=false;const p=ui(async url=>{if(fail)throw Error("网络断开");return ok(url.includes("/bootstrap")?bootstrap():overview());});await p.run("load()");fail=true;await p.run("load()");assert.equal(p.elements.get("plays").textContent,"2");assert.equal(p.elements.get("error").hidden,false);assert.match(p.elements.get("errorText").textContent,/保留上次/);assert.equal(p.elements.get("refresh").disabled,false);
});
test("record view receives clamped page and matching total summary",async()=>{
  const p=ui(async url=>ok(url.includes("/bootstrap")?bootstrap():{mode:"records",timeZone:"UTC",rows:[],total:0,page:1,pages:1,stats:{seconds:0,completed:0}}));await p.run('state.screen="records";state.page=99;load()');assert.equal(p.run("state.page"),1);assert.equal(p.elements.get("listEmpty").hidden,false);assert.equal(p.elements.get("next").disabled,true);
});
test("read-only detail and embedded page do not alter the host iframe",async()=>{
  const p=ui(async url=>ok(url.includes("/bootstrap")?bootstrap():overview()),{embedded:true});Object.defineProperty(p.window,"frameElement",{get(){throw Error("must not access parent frame");}});await p.run("start()");assert.equal(p.elements.get("standalone").hidden,false);assert.equal(p.elements.get("plays").textContent,"2");assert.match(p.run('detailMarkup({id:"r1",itemName:"电影"},null,false)'),/只读|历史记录快照/);
});
test("destructive operations require manage mode and explicit confirmation",async()=>{
  let writes=0;const p=ui(async(_,options)=>{if(options.method!=="GET")writes++;return ok({});});p.run('state.rows.set("r1",{id:"r1",itemName:"电影"})');p.run('deleteRecord("r1");clearRecords()');assert.equal(writes,0);p.elements.get("manage").checked=true;p.run('deleteRecord("r1");clearRecords()');assert.equal(writes,0);
});
test("static page loads no external assets and contains the controller's required elements",()=>{
  assert.ok(!/https?:\/\//.test(html));assert.match(html,/<script src="\.\/theme.js"><\/script>/);assert.match(html,/<script src="\.\/app.js" type="module">/);
  const ids=new Set([...html.matchAll(/id="([^"]+)"/g)].map(match=>match[1]));for(const match of app.matchAll(/\$\("([^"]+)"\)/g)){assert.ok(ids.has(match[1])||match[1]==="detailHistory",`missing #${match[1]}`);}
  assert.ok(!app.includes("frameElement"));assert.ok(!app.includes("/Items/"));
});

test("confirmed management captures the selected user and restores controls after a write",async()=>{
  const writes=[];const p=ui(async(url,options)=>{if(options.method!=="GET"){writes.push({url,options});return ok({removed:1});}return ok(url.includes("/bootstrap")?bootstrap():overview());},{confirm:true});
  await p.run("start()");p.elements.get("manage").checked=true;await p.run('state.user="name:alice";clearRecords()');
  assert.equal(writes.length,1);assert.deepEqual(JSON.parse(writes[0].options.body),{user:"name:alice",confirm:true});assert.equal(p.run("state.writing"),false);
});

test("metadata preview uses the official plugin endpoint and does not expose a filesystem editor",async()=>{
  const calls=[];const p=ui(async(url,options)=>{calls.push({url,options});return ok({available:true,record:{id:"r1",itemName:"历史名",itemId:"1"},item:{id:"1",name:"媒体库片名",type:"Movie",year:2026,providerIds:{Tmdb:"123"}}});});
  p.run('state.rows.set("r1",{id:"r1",itemName:"历史名",itemId:"1"})');await p.run('openDetail("r1")');
  assert.equal(calls[0].url,"/history/media/r1");assert.match(p.elements.get("detailContent").innerHTML,/媒体库片名/);assert.match(p.elements.get("detailContent").innerHTML,/只读预览/);assert.ok(!p.elements.get("detailContent").innerHTML.includes('type="file"'));
});
