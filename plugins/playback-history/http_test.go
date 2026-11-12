package main

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func TestPagePrefixAndSameOriginPolicy(t *testing.T) {
	a := &application{}
	routes := a.routes()
	for _, prefix := range []string{"", "/emby", "/mediabrowser"} {
		resp := routes["GET /history"](nil, &sdk.Request{User: &sdk.User{IsAdmin: true}, Path: "/history", OriginalPath: prefix + "/history"})
		if resp.Status != 200 || !strings.Contains(string(resp.Body), `<base href="`+prefix+`/history/">`) {
			t.Fatal("页面丢失前缀", prefix)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
			t.Fatal("未允许同源插件页面")
		}
	}
	for _, name := range []string{"app.js", "core.mjs", "theme.js", "styles.css"} {
		resp := routes["GET /history/"+name](nil, &sdk.Request{User: &sdk.User{IsAdmin: true}})
		if resp.Status != 200 || len(resp.Body) == 0 {
			t.Fatal("静态资源缺失", name)
		}
	}
	if resp := routes["GET /history/overview"](nil, &sdk.Request{User: &sdk.User{IsAdmin: true}}); resp.Status != 503 {
		t.Fatal("未初始化时不应返回空成功结果")
	}
}
func TestPaginationClampsAfterDeletionAndTotalsUseAllFilteredRows(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	for i := 0; i < 25; i++ {
		e := event(base.Add(-time.Duration(i)*time.Minute), string(rune('A'+i)), "100")
		apply(t, s, "playback.started", e)
	}
	a := &application{store: s}
	r := &sdk.Request{User: &sdk.User{IsAdmin: true}, Query: url.Values{"page": {"999"}}}
	resp := a.routes()["GET /history/records"](nil, r)
	var data struct {
		Page  int
		Pages int
		Total int
		Rows  []Record
		Stats Totals
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		t.Fatal(err)
	}
	if data.Page != 2 || data.Pages != 2 || data.Total != 25 || len(data.Rows) != 5 || data.Stats.Plays != 25 {
		t.Fatalf("分页或全量统计错误: %+v", data)
	}
}
func TestManagementRequiresExplicitConfirmation(t *testing.T) {
	s := testStore(t)
	apply(t, s, "playback.started", event(time.Now().UTC(), "s1", "100"))
	a := &application{store: s}
	handler := a.routes()["POST /history/clear"]
	for _, body := range []string{`{}`, `{"confirm":false}`, `invalid`} {
		if handler(nil, &sdk.Request{User: &sdk.User{IsAdmin: true}, Body: []byte(body)}).Status != 400 {
			t.Fatal("未确认的请求清空了记录")
		}
	}
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 {
		t.Fatal("无效请求改变了记录")
	}
	if handler(nil, &sdk.Request{User: &sdk.User{IsAdmin: true}, Body: []byte(`{"confirm":true,"user":"name:someone-else"}`)}).Status != 200 {
		t.Fatal("空范围清理应该可重复执行")
	}
	rows, _, _ = s.Snapshot()
	if len(rows) != 1 {
		t.Fatal("清理其他用户时误删记录")
	}
}
func TestManifestRoutesMatchImplementation(t *testing.T) {
	body, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Routes []struct {
			Method string
			Path   string
			Auth   string
		}
		Page struct{ Path string }
	}
	if err = json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	routes := (&application{}).routes()
	for _, route := range manifest.Routes {
		if route.Auth != "admin" {
			t.Fatal("所有路由必须要求管理员", route.Path)
		}
		key := route.Method + " " + route.Path
		if routes[key] == nil {
			t.Fatal("manifest 路由未实现", key)
		}
	}
	if len(routes) != len(manifest.Routes) || routes["GET "+manifest.Page.Path] == nil {
		t.Fatal("实现路由与安装声明不一致")
	}
}

func TestAllRoutesRejectAnonymousAndNonAdminBeforeProcessing(t *testing.T) {
	s := testStore(t)
	id := apply(t, s, "playback.started", event(time.Now().UTC(), "s1", "100"))
	a := &application{store: s}
	for key, handler := range a.routes() {
		for _, tc := range []struct {
			name   string
			user   *sdk.User
			status int
		}{
			{"anonymous", nil, 401},
			{"regular user", &sdk.User{ID: "u1", IsAdmin: false}, 403},
		} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				response := handler(nil, &sdk.Request{User: tc.user, Params: map[string]string{"id": id}, Query: url.Values{}, Body: []byte(`{"confirm":true}`)})
				if response.Status != tc.status {
					t.Fatalf("status=%d want %d", response.Status, tc.status)
				}
				if strings.Contains(string(response.Body), "测试电影") {
					t.Fatal("unauthorized response exposed history")
				}
			})
		}
	}
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 {
		t.Fatal("unauthorized delete/clear changed history")
	}
}
