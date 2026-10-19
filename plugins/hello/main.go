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
		},

		OnShutdown: func(h *sdk.Host) {
			h.Info("示例插件停止", "requests", requests.Load())
		},
	})
}
