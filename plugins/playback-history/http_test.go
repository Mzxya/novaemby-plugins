package main

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func TestPageDirectListWithoutAuthenticationOrRedirect(t *testing.T) {
	a := &application{store: openTestStore(t)}
	handler := a.routes()["GET /history"]
	resp := handler(nil, &sdk.Request{OriginalPath: "/history", Path: "/history"})
	if resp.Status != http.StatusOK {
		t.Fatalf("页面状态 = %d，应为 200 而非跳转", resp.Status)
	}
	body := string(resp.Body)
	if !strings.Contains(body, `<base href="/history/">`) || !strings.Contains(body, `id="recordList"`) || strings.Contains(body, `id="authView"`) {
		t.Fatalf("页面不是直接查询列表：%s", body[:min(len(body), 300)])
	}
}

func TestRecordsAPIWithoutAuthentication(t *testing.T) {
	s := openTestStore(t)
	a := &application{store: s}
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	rec := makeRecord("s1", "u1", "张三", "1", "公开查询", "Movie", at)
	if _, _, err := s.Start(rec); err != nil {
		t.Fatal(err)
	}
	req := &sdk.Request{Query: url.Values{"page": {"1"}, "pageSize": {"30"}}}
	resp := a.routes()["GET /history/records"](nil, req)
	if resp.Status != http.StatusOK || !bytes.Contains(resp.Body, []byte("公开查询")) {
		t.Fatalf("免验证查询失败：%d %s", resp.Status, resp.Body)
	}
}

func TestParseQuery(t *testing.T) {
	req := &sdk.Request{Query: url.Values{"from": {"2026-11-01"}, "to": {"2026-11-07"}, "status": {"completed"}, "page": {"2"}, "pageSize": {"50"}}}
	q, err := parseQuery(req)
	if err != nil || q.Page != 2 || q.PageSize != 50 || q.Status != "completed" || q.From.IsZero() || q.To.IsZero() {
		t.Fatalf("查询参数解析不正确：%+v, %v", q, err)
	}
	if q, err := parseQuery(&sdk.Request{Query: url.Values{"sort": {"asc"}}}); err != nil || q.SortBy != "oldest" {
		t.Fatalf("旧升序参数失效：%+v, %v", q, err)
	}
	if _, err := parseQuery(&sdk.Request{Query: url.Values{"page": {"0"}}}); err == nil {
		t.Fatal("无效页码应返回错误")
	}
}

func TestThemeAssetAndDefaultLightPage(t *testing.T) {
	a := &application{}
	page := servePage(&sdk.Request{OriginalPath: "/emby/history"})
	body := string(page.Body)
	if !strings.Contains(body, `data-theme="light"`) || !strings.Contains(body, `id="themeButton"`) {
		t.Fatal("页面缺少默认亮色主题或切换入口")
	}
	script, style := strings.Index(body, `<script src="./theme.js">`), strings.Index(body, `<link rel="stylesheet"`)
	if script < 0 || style < 0 || script > style {
		t.Fatal("主题初始化必须在样式表之前加载")
	}
	route := a.routes()["GET /history/theme.js"]
	if route == nil {
		t.Fatal("主题脚本未注册路由")
	}
	asset := route(nil, &sdk.Request{})
	if asset.Status != http.StatusOK || !bytes.Contains(asset.Body, []byte("window.NovaTheme")) {
		t.Fatal("主题脚本未嵌入安装程序")
	}
}
