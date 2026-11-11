// hello 示例插件：演示 NovaEmby 插件的每种能力。写新插件时复制这个目录，改 manifest.json 和这个文件即可。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

// requests 累计请求数，保存在数据目录里（演示数据目录：升级插件后仍然保留）。
var requests atomic.Int64

func countFile(h *sdk.Host) string { return filepath.Join(h.DataDir(), "count.txt") }

func main() {
	sdk.Run(&sdk.Plugin{
		// 启动：读取上次保存的计数
		OnInit: func(h *sdk.Host) error {
			if b, err := os.ReadFile(countFile(h)); err == nil {
				n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
				requests.Store(n)
			}
			h.Info("示例插件已启动", "server", h.Server().Name, "greeting", h.Config().String("greeting"))
			return nil
		},

		// 管理员修改配置：可以在这里做 manifest 表达不了的检查，返回错误就不保存
		OnConfigure: func(h *sdk.Host, cfg sdk.Config) error {
			if strings.Contains(cfg.String("greeting"), "<") {
				return errors.New("问候语不能包含 <")
			}
			h.Info("配置已更新", "greeting", cfg.String("greeting"))
			return nil
		},

		// 事件：manifest 的 events 里订阅了 playback.started、metadata.item_ready
		OnEvent: func(h *sdk.Host, ev sdk.Event) {
			switch ev.Name {
			case "playback.started":
				var e struct {
					Session struct {
						UserName   string
						DeviceName string
					}
					ItemName string
				}
				if ev.Decode(&e) != nil {
					return
				}
				h.Info("开始播放", "user", e.Session.UserName, "item", e.ItemName)
				if h.Config().Bool("logPlayback") {
					_ = h.Activity(context.Background(), fmt.Sprintf("%s 在 %s 上开始播放 %s", e.Session.UserName, e.Session.DeviceName, e.ItemName), "", "info")
				}
			case "metadata.item_ready":
				var e struct {
					Name string
					Type string
				}
				if ev.Decode(&e) == nil {
					h.Debug("新片入库", "name", e.Name, "type", e.Type)
				}
			}
		},

		// HTTP 接口：键与 manifest 的 routes 一一对应
		Routes: map[string]sdk.HandlerFunc{
			// 任何人都能访问：/api/hello/张三、/emby/api/hello/张三
			"GET /api/hello/{name}": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
				n := requests.Add(1)
				_ = os.WriteFile(countFile(h), []byte(strconv.FormatInt(n, 10)), 0o644)
				cfg := h.Config()
				msg := strings.Repeat(cfg.String("greeting"), int(max(1, cfg.Int("repeat")))) + "，" + r.Params["name"]
				return sdk.JSON(http.StatusOK, map[string]any{"message": msg, "server": h.Server().Name, "requests": n})
			},
			// 需要登录：读取条目信息
			"GET /api/hello-item/{id}": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
				it, err := h.Item(r.Context, r.Params["id"])
				if errors.Is(err, sdk.ErrNotFound) {
					return sdk.Text(http.StatusNotFound, "Item not found.")
				}
				if err != nil {
					return sdk.Text(http.StatusBadGateway, err.Error())
				}
				title := it.Name
				if it.SeriesName != "" && it.SeasonNumber != nil && it.EpisodeNumber != nil {
					title = fmt.Sprintf("%s S%02dE%02d %s", it.SeriesName, *it.SeasonNumber, *it.EpisodeNumber, it.Name)
				}
				return sdk.JSON(http.StatusOK, map[string]any{"title": title, "viewer": r.User.Name, "item": it})
			},
			// 只给管理员
			"GET /api/hello-stats": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
				b, _ := json.Marshal(map[string]any{"requests": requests.Load(), "dataDir": h.DataDir(), "version": h.Version()})
				return sdk.Bytes(http.StatusOK, "application/json; charset=utf-8", b)
			},
			// 管理员：把访问计数清零（POST，演示插件网页里怎样发修改类请求）
			"POST /api/hello-stats/reset": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
				requests.Store(0)
				_ = os.WriteFile(countFile(h), []byte("0"), 0o644)
				return sdk.JSON(http.StatusOK, map[string]any{"requests": 0})
			},
			// 插件自己的网页（manifest 的 page）：Web 左侧“插件”菜单里内嵌打开。
			// 页面与 NovaEmby 同源、带登录 Cookie，所以页面里的 fetch 直接调用本插件的接口；
			// 修改类请求（POST 等）要在 X-Nova-CSRF 头里带上 nova_csrf cookie 的值。
			"GET /api/hello-page": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
				return sdk.Bytes(http.StatusOK, "text/html; charset=utf-8", []byte(helloPage))
			},
		},

		// 元数据来源（manifest 的 metadata）：媒体库在选项中启用了这个插件后，刮削时为每个条目调用。
		// 示例只在条目没有标签时补一个标签（配置项 metadataTag），并演示怎样按条目信息判断。
		// 真实的插件在这里按名称、年份、外部 ID 去查询自己的数据源，联网请用 r.Context。
		OnMetadata: func(h *sdk.Host, r *sdk.MetadataRequest) (*sdk.Metadata, error) {
			tag := strings.TrimSpace(h.Config().String("metadataTag"))
			if tag == "" {
				return nil, nil // 没有可提供的数据
			}
			h.Debug("提供元数据", "type", r.Type, "name", r.Name, "language", r.Language)
			return &sdk.Metadata{Tags: []string{tag}}, nil
		},

		// 计划任务（manifest 的 tasks）：键与 manifest 中任务的 key 一致。
		Tasks: map[string]sdk.TaskFunc{
			// 把请求计数写进活动日志；计数为 0 时跳过（演示 sdk.Skip）
			"Report": func(h *sdk.Host, t *sdk.Task) (string, error) {
				n := requests.Load()
				if n == 0 {
					return "", sdk.Skip("还没有人访问过 /api/hello")
				}
				for i := 1; i <= 4; i++ { // 演示进度和取消
					select {
					case <-t.Context.Done():
						return "", t.Context.Err()
					case <-time.After(250 * time.Millisecond):
					}
					t.Progress(float64(i) * 25)
				}
				msg := fmt.Sprintf("/api/hello 一共被访问了 %d 次", n)
				_ = h.Activity(t.Context, msg, "", "info")
				return msg, nil
			},
		},

		OnShutdown: func(h *sdk.Host) {
			h.Info("示例插件停止", "requests", requests.Load())
		},
	})
}

// helloPage 插件网页（单个 HTML，不依赖外部文件）。跟随系统的浅色 / 深色。
const helloPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>示例插件</title>
<style>
  :root { color-scheme: light dark; --bg: #f4f4f5; --card: #fff; --text: #18181b; --muted: #71717a; --border: #e4e4e7; }
  @media (prefers-color-scheme: dark) { :root { --bg: #18181b; --card: #232326; --text: #f4f4f5; --muted: #a1a1aa; --border: #333338; } }
  body { margin: 0; padding: 16px; background: var(--bg); color: var(--text); font: 15px/1.5 system-ui, sans-serif; }
  .card { max-width: 520px; background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 20px; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  p { color: var(--muted); margin: 0 0 16px; }
  .n { font-size: 40px; font-weight: 700; }
  button { margin-top: 12px; padding: 8px 16px; border-radius: 10px; border: 1px solid var(--border); background: var(--card); color: var(--text); font: inherit; cursor: pointer; }
  .err { color: #dc2626; }
</style>
</head>
<body>
<div class="card">
  <h1>示例插件</h1>
  <p>/api/hello 被访问的次数（插件自己的网页示例）</p>
  <div class="n" id="n">…</div>
  <button id="reset">清零</button>
  <div id="msg"></div>
</div>
<script>
  // 修改类请求需要 X-Nova-CSRF：值在 nova_csrf cookie 里
  function csrf() {
    const m = document.cookie.match(/(?:^|; )nova_csrf=([^;]*)/)
    return m ? decodeURIComponent(m[1]) : ''
  }
  const msg = document.getElementById('msg')
  async function load() {
    const res = await fetch('/api/hello-stats')
    if (!res.ok) { msg.className = 'err'; msg.textContent = '读取失败：' + res.status; return }
    document.getElementById('n').textContent = (await res.json()).requests
  }
  document.getElementById('reset').onclick = async () => {
    const res = await fetch('/api/hello-stats/reset', { method: 'POST', headers: { 'X-Nova-CSRF': csrf() } })
    msg.className = res.ok ? '' : 'err'
    msg.textContent = res.ok ? '已清零' : '失败：' + res.status
    load()
  }
  load()
</script>
</body>
</html>
`
