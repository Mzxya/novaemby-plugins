package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

//go:embed assets/*
var assets embed.FS

func fail(status int,message string)*sdk.Response{return sdk.JSON(status,map[string]string{"error":message})}
func page(r *sdk.Request)*sdk.Response {
	body,err:=assets.ReadFile("assets/index.html");if err!=nil{return fail(500,"页面资源不可用")}
	path:=first(r.OriginalPath,r.Path);if path==""{path="/history"}
	base:=strings.TrimSuffix(path,"/")+"/"
	body=bytes.Replace(body,[]byte("<head>"),[]byte(`<head><base href="`+html.EscapeString(base)+`">`),1)
	resp:=sdk.Bytes(200,"text/html; charset=utf-8",body)
	resp.Header.Set("Content-Security-Policy","default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; base-uri 'self'; object-src 'none'; form-action 'self'")
	return resp
}
func (a *application) routes()map[string]sdk.HandlerFunc {
	routes:=map[string]sdk.HandlerFunc{
		"GET /history":func(_ *sdk.Host,r *sdk.Request)*sdk.Response{return page(r)},
		"GET /history/":func(_ *sdk.Host,r *sdk.Request)*sdk.Response{return page(r)},
		"GET /history/bootstrap":a.ready(a.bootstrap),
		"GET /history/overview":a.ready(a.overview),
		"GET /history/records":a.ready(a.records),
		"GET /history/media/{id}":a.ready(a.media),
		"GET /history/poster/{id}":a.ready(a.poster),
		"DELETE /history/records/{id}":a.ready(a.delete),
		"POST /history/clear":a.ready(a.clear),
	}
	for _,asset:=range []struct{name,mime string}{{"app.js","text/javascript; charset=utf-8"},{"theme.js","text/javascript; charset=utf-8"},{"core.mjs","text/javascript; charset=utf-8"},{"styles.css","text/css; charset=utf-8"}}{
		name,mime:=asset.name,asset.mime
		routes["GET /history/"+name]=func(_ *sdk.Host,_ *sdk.Request)*sdk.Response{body,err:=assets.ReadFile("assets/"+name);if err!=nil{return fail(404,"资源不存在")};return sdk.Bytes(200,mime,body)}
	}
	for key,handler:=range routes {next:=handler;routes[key]=func(h *sdk.Host,r *sdk.Request)*sdk.Response{resp:=next(h,r);if resp.Header==nil{resp.Header=http.Header{}};if resp.Header.Get("Cache-Control")==""{resp.Header.Set("Cache-Control","no-store")};resp.Header.Set("X-Content-Type-Options","nosniff");resp.Header.Set("Referrer-Policy","same-origin");return resp}}
	return routes
}
func (a *application) ready(next sdk.HandlerFunc)sdk.HandlerFunc{return func(h *sdk.Host,r *sdk.Request)*sdk.Response{if a.store==nil{return fail(503,"播放历史尚未就绪，请稍后重试")};return next(h,r)}}
func (a *application) bootstrap(h *sdk.Host,_ *sdk.Request)*sdk.Response {
	rows,users,now:=a.store.Snapshot();template:="";if h!=nil{template=h.Config().String("posterPathTemplate");if validatePosterTemplate(template)!=nil{template=""}}
	return sdk.JSON(200,map[string]any{"version":a.version,"server":a.server,"timeZone":a.store.zone.String(),"users":users,"records":len(rows),"at":now,"unsupportedEvents":a.unsupported.Load(),"posterTemplate":template,"capabilities":map[string]bool{"metadataPreview":true,"metadataEdit":false,"sidecarArtwork":true,"hostArtworkAPI":false,"historyReplay":false}})
}
func (a *application) overview(_ *sdk.Host,r *sdk.Request)*sdk.Response {
	days:=30;if raw:=r.Query.Get("days");raw!=""{n,err:=strconv.Atoi(raw);if err!=nil||n!=7&&n!=30&&n!=90{return fail(400,"时间范围仅支持 7、30、90 天")};days=n}
	rows,_,now:=a.store.Snapshot();return sdk.JSON(200,makeOverview(rows,now,a.store.zone,r.Query.Get("user"),days))
}
func (a *application) records(_ *sdk.Host,r *sdk.Request)*sdk.Response {
	filter,err:=parseFilter(r.Query,a.store.zone);if err!=nil{return fail(400,err.Error())}
	rows,_,_:=a.store.Snapshot();rows=filterRecords(rows,filter);stats:=totals(rows)
	result:=map[string]any{"stats":stats,"mode":filter.Mode,"timeZone":a.store.zone.String()}
	if filter.Mode=="items"{groups:=groupContents(rows,filter.Sort);total:=len(groups);pages:=max(1,(total+filter.Size-1)/filter.Size);current:=min(filter.Page,pages);start:=(current-1)*filter.Size;result["rows"]=groups[start:min(start+filter.Size,total)];result["total"]=total;result["pages"]=pages;result["page"]=current
	}else{sortRecords(rows,filter.Sort);total:=len(rows);pages:=max(1,(total+filter.Size-1)/filter.Size);current:=min(filter.Page,pages);start:=(current-1)*filter.Size;result["rows"]=rows[start:min(start+filter.Size,total)];result["total"]=total;result["pages"]=pages;result["page"]=current}
	return sdk.JSON(200,result)
}
func (a *application) delete(_ *sdk.Host,r *sdk.Request)*sdk.Response {
	deleted,err:=a.store.Delete(r.Params["id"]);if err!=nil{return fail(500,"删除失败，原记录保留")};if !deleted{return fail(404,"记录已不存在")};return sdk.JSON(200,map[string]bool{"deleted":true})
}
func (a *application) clear(_ *sdk.Host,r *sdk.Request)*sdk.Response {
	var body struct{User string `json:"user"`;Confirm bool `json:"confirm"`}
	if len(r.Body)>4096||json.Unmarshal(r.Body,&body)!=nil||!body.Confirm{return fail(400,"清空记录需要明确确认")}
	n,err:=a.store.Clear(body.User);if err!=nil{return fail(500,"清空失败，请检查插件日志")};return sdk.JSON(200,map[string]int{"removed":n})
}
