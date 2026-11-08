package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

//go:embed assets/index.html assets/app.js assets/styles.css assets/theme.js
var webAssets embed.FS

func (a *application) routes() map[string]sdk.HandlerFunc {
	page := func(_ *sdk.Host, r *sdk.Request) *sdk.Response { return servePage(r) }
	return map[string]sdk.HandlerFunc{
		"GET /history":  page,
		"GET /history/": page,
		"GET /history/theme.js": func(_ *sdk.Host, _ *sdk.Request) *sdk.Response {
			return serveAsset("assets/theme.js", "text/javascript; charset=utf-8")
		},
		"GET /history/app.js": func(_ *sdk.Host, _ *sdk.Request) *sdk.Response {
			return serveAsset("assets/app.js", "text/javascript; charset=utf-8")
		},
		"GET /history/styles.css": func(_ *sdk.Host, _ *sdk.Request) *sdk.Response {
			return serveAsset("assets/styles.css", "text/css; charset=utf-8")
		},
		"GET /history/dashboard":       a.publicRoute(a.handleDashboard),
		"GET /history/users":           a.publicRoute(a.handleUsers),
		"GET /history/records":         a.publicRoute(a.handleRecords),
		"GET /history/items":           a.publicRoute(a.handleItems),
		"DELETE /history/records/{id}": a.publicRoute(a.handleDelete),
		"POST /history/clear":          a.publicRoute(a.handleClear),
	}
}

func servePage(r *sdk.Request) *sdk.Response {
	b, err := webAssets.ReadFile("assets/index.html")
	if err != nil {
		return sdk.Text(http.StatusNotFound, "Not found.")
	}
	base := strings.TrimSuffix(r.OriginalPath, "/") + "/"
	b = bytes.Replace(b, []byte("<head>"), []byte(`<head><base href="`+html.EscapeString(base)+`">`), 1)
	resp := sdk.Bytes(http.StatusOK, "text/html; charset=utf-8", b)
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Set("X-Content-Type-Options", "nosniff")
	resp.Header.Set("Referrer-Policy", "same-origin")
	resp.Header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: http: https:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'self'")
	return resp
}

func serveAsset(name, contentType string) *sdk.Response {
	b, err := webAssets.ReadFile(name)
	if err != nil {
		return sdk.Text(http.StatusNotFound, "Not found.")
	}
	resp := sdk.Bytes(http.StatusOK, contentType, b)
	// 静态资源随插件版本变化，禁用缓存，避免升级后仍显示旧页面（例如主题切换）。
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Set("X-Content-Type-Options", "nosniff")
	resp.Header.Set("Referrer-Policy", "same-origin")
	return resp
}

// publicRoute 按需求开放查询和管理接口；请只在可信内网中使用。
func (a *application) publicRoute(next sdk.HandlerFunc) sdk.HandlerFunc {
	return func(h *sdk.Host, r *sdk.Request) *sdk.Response {
		if a.store == nil {
			return jsonError(http.StatusServiceUnavailable, "数据存储尚未就绪")
		}
		resp := next(h, r)
		if resp != nil {
			if resp.Header == nil {
				resp.Header = http.Header{}
			}
			resp.Header.Set("Cache-Control", "no-store")
			resp.Header.Set("X-Content-Type-Options", "nosniff")
		}
		return resp
	}
}

func jsonError(status int, message string) *sdk.Response {
	return sdk.JSON(status, map[string]string{"error": message})
}

func parseQuery(r *sdk.Request) (Query, error) {
	q := Query{
		UserKey: strings.TrimSpace(r.Query.Get("user")), Search: strings.TrimSpace(r.Query.Get("q")),
		ItemType: strings.TrimSpace(r.Query.Get("type")), Status: strings.TrimSpace(r.Query.Get("status")),
		Page: 1, PageSize: 30,
	}
	switch strings.ToLower(strings.TrimSpace(r.Query.Get("sort"))) {
	case "", "recent", "desc":
		q.SortBy = "recent"
	case "asc", "oldest":
		q.SortBy = "oldest"
	case "duration":
		q.SortBy = "duration"
	case "count":
		q.SortBy = "count"
	default:
		return q, fmt.Errorf("sort 只支持 recent、oldest、duration、count")
	}
	var err error
	if raw := r.Query.Get("page"); raw != "" {
		q.Page, err = strconv.Atoi(raw)
		if err != nil || q.Page < 1 {
			return q, fmt.Errorf("page 必须是大于 0 的整数")
		}
	}
	if raw := r.Query.Get("pageSize"); raw != "" {
		q.PageSize, err = strconv.Atoi(raw)
		if err != nil || q.PageSize < 1 || q.PageSize > 500 {
			return q, fmt.Errorf("pageSize 必须在 1–500 之间")
		}
	}
	switch q.Status {
	case "", "all", "completed", "incomplete", "active":
	default:
		return q, fmt.Errorf("status 只支持 all、completed、incomplete、active")
	}
	q.From, err = parseTime(r.Query.Get("from"), false)
	if err != nil {
		return q, fmt.Errorf("from 日期格式不正确")
	}
	q.To, err = parseTime(r.Query.Get("to"), true)
	if err != nil {
		return q, fmt.Errorf("to 日期格式不正确")
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return q, fmt.Errorf("开始日期不能晚于结束日期")
	}
	return q, nil
}

func parseTime(raw string, endOfDay bool) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if len(raw) == len("2006-01-02") {
		t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
		if err != nil {
			return time.Time{}, err
		}
		if endOfDay {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func (a *application) handleUsers(_ *sdk.Host, _ *sdk.Request) *sdk.Response {
	users := a.store.Users()
	if users == nil {
		users = []UserSummary{}
	}
	return sdk.JSON(http.StatusOK, map[string]any{"users": users})
}

func (a *application) handleRecords(_ *sdk.Host, r *sdk.Request) *sdk.Response {
	q, err := parseQuery(r)
	if err != nil {
		return jsonError(http.StatusBadRequest, err.Error())
	}
	return sdk.JSON(http.StatusOK, a.store.Query(q))
}

// handleItems 返回「按内容汇总」视图：同一部影片/剧集单集的多次播放合并为一行。
func (a *application) handleItems(_ *sdk.Host, r *sdk.Request) *sdk.Response {
	q, err := parseQuery(r)
	if err != nil {
		return jsonError(http.StatusBadRequest, err.Error())
	}
	if q.SortBy == "oldest" {
		q.SortBy = "recent" // 汇总视图按最早播放排序没有意义
	}
	return sdk.JSON(http.StatusOK, a.store.Items(q))
}

func (a *application) handleDelete(_ *sdk.Host, r *sdk.Request) *sdk.Response {
	deleted, err := a.store.Delete(r.Params["id"])
	if err != nil {
		return jsonError(http.StatusInternalServerError, "删除记录失败："+err.Error())
	}
	if !deleted {
		return jsonError(http.StatusNotFound, "记录不存在")
	}
	return sdk.JSON(http.StatusOK, map[string]any{"deleted": true, "remaining": a.store.Count()})
}

func (a *application) handleClear(_ *sdk.Host, r *sdk.Request) *sdk.Response {
	var body struct {
		UserKey string `json:"userKey"`
		Before  string `json:"before"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return jsonError(http.StatusBadRequest, "请求体不是有效的 JSON")
	}
	if !body.Confirm {
		return jsonError(http.StatusBadRequest, "清理记录需要 confirm=true 确认")
	}
	before, err := parseTime(body.Before, true)
	if err != nil {
		return jsonError(http.StatusBadRequest, "before 日期格式不正确")
	}
	removed, err := a.store.Clear(strings.TrimSpace(body.UserKey), before)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "清理记录失败："+err.Error())
	}
	return sdk.JSON(http.StatusOK, map[string]any{"removed": removed, "remaining": a.store.Count()})
}

func (a *application) handleDashboard(_ *sdk.Host, r *sdk.Request) *sdk.Response {
	days := 30
	if raw := r.Query.Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || (n != 7 && n != 30 && n != 90) {
			return jsonError(http.StatusBadRequest, "days 只支持 7、30、90")
		}
		days = n
	}
	return sdk.JSON(http.StatusOK, a.store.Dashboard(strings.TrimSpace(r.Query.Get("user")), days))
}
