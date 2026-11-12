package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

type application struct {
	store       *Store
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	jobs        chan string
	queuedMu    sync.Mutex
	queued      map[string]bool
	unsupported atomic.Uint64
	rawEvents   atomic.Uint64
	server      string
	version     string
}

func cfgBool(c sdk.Config, key string, fallback bool) bool {
	v, ok := c[key].(bool)
	if ok {
		return v
	}
	return fallback
}
func cfgInt(c sdk.Config, key string, fallback int) int {
	if _, ok := c[key]; ok {
		return int(c.Int(key))
	}
	return fallback
}
func validateConfig(c sdk.Config) error {
	for _, rule := range []struct {
		key       string
		low, high int
	}{{"retentionDays", 0, 3650}, {"maxRecords", 1000, 500000}, {"completionPercent", 50, 100}} {
		if raw, ok := c[rule.key]; ok {
			switch v := raw.(type) {
			case float64:
				if v != float64(int64(v)) {
					return fmt.Errorf("%s 必须为整数", rule.key)
				}
			case int, int64:
			default:
				return fmt.Errorf("%s 必须为整数", rule.key)
			}
			n := int(c.Int(rule.key))
			if n < rule.low || n > rule.high {
				return fmt.Errorf("%s 超出范围", rule.key)
			}
		}
	}
	return validatePosterTemplate(c.String("posterPathTemplate"))
}
func main() {
	a := &application{}
	sdk.Run(&sdk.Plugin{
		OnInit: func(h *sdk.Host) error {
			if err := validateConfig(h.Config()); err != nil {
				return err
			}
			zone, valid := resolveZone(h.TimeZone())
			if !valid {
				h.Warn("未提供有效宿主时区，使用 UTC", "timeZone", h.TimeZone())
			}
			store, err := OpenStore(filepath.Join(h.DataDir(), "history.jsonl"), zone)
			if err != nil {
				return err
			}
			a.store = store
			a.server = h.Server().Name
			a.version = h.Version()
			a.ctx, a.cancel = context.WithCancel(context.Background())
			a.jobs = make(chan string, 128)
			a.queued = map[string]bool{}
			a.wg.Add(2)
			go a.worker(h)
			go a.flushWorker(h)
			_, err = store.Cleanup(cfgInt(h.Config(), "retentionDays", 365), cfgInt(h.Config(), "maxRecords", 50000))
			if err != nil {
				h.Warn("历史清理失败，原记录保留", "error", err.Error())
			}
			h.Info("播放历史已启动", "version", a.version, "timeZone", zone.String())
			return nil
		},
		OnConfigure: func(_ *sdk.Host, c sdk.Config) error { return validateConfig(c) },
		OnEvent:     func(h *sdk.Host, e sdk.Event) { a.event(h, e) },
		Routes:      a.routes(),
		Tasks: map[string]sdk.TaskFunc{
			"Cleanup": func(h *sdk.Host, t *sdk.Task) (string, error) {
				if a.store == nil {
					return "", sdk.Skip("插件尚未就绪")
				}
				if err := t.Context.Err(); err != nil {
					return "", err
				}
				t.Progress(10)
				n, err := a.store.Cleanup(cfgInt(h.Config(), "retentionDays", 365), cfgInt(h.Config(), "maxRecords", 50000))
				t.Progress(100)
				return fmt.Sprintf("清理 %d 条历史", n), err
			},
			"RepairMetadata": func(h *sdk.Host, t *sdk.Task) (string, error) { return a.repair(h, t) },
		},
		OnShutdown: func(h *sdk.Host) {
			if a.cancel != nil {
				a.cancel()
				a.wg.Wait()
			}
			if a.store != nil {
				if err := a.store.Close(); err != nil {
					h.Error("历史保存失败", "error", err.Error())
				}
			}
		},
	})
}
func (a *application) event(h *sdk.Host, e sdk.Event) {
	if cfgBool(h.Config(), "verboseEventLog", false) {
		logRawEvent(e, a.rawEvents.Add(1), h.Info)
	}
	if a.store == nil {
		return
	}
	if strings.HasPrefix(e.Name, "user.") {
		var raw struct {
			UserID   any
			UserName string
		}
		if e.Decode(&raw) != nil {
			return
		}
		// Event's numeric user ID is uncommon; standard SDK documentation uses strings.
		id, _ := raw.UserID.(string)
		if err := a.store.UserEvent(User{ID: id, Name: raw.UserName, Deleted: e.Name == "user.deleted"}); err != nil {
			h.Warn("保存用户事件失败", "error", err.Error())
		}
		return
	}
	if e.Name != "playback.started" && e.Name != "playback.paused" && e.Name != "playback.progress" && e.Name != "playback.stopped" {
		return
	}
	data, err := decodePlayback(e.Data, e.At)
	if err != nil {
		n := a.unsupported.Add(1)
		if n == 1 || n%100 == 0 {
			h.Warn("播放事件无法关联，未猜测媒体字段", "count", n, "error", err.Error())
		}
		return
	}
	c := h.Config()
	data.SkipNew = data.ItemType == "Movie" && !cfgBool(c, "recordMovies", true) || data.ItemType == "Episode" && !cfgBool(c, "recordEpisodes", true) || data.ItemType != "" && data.ItemType != "Movie" && data.ItemType != "Episode" && !cfgBool(c, "recordOther", false)
	id, _, err := a.store.Apply(e.Name, data, cfgInt(h.Config(), "completionPercent", 90))
	if err != nil {
		h.Error("播放记录保存失败", "error", err.Error())
		return
	}
	if id != "" && (e.Name == "playback.started" || e.Name == "playback.stopped") {
		a.enqueue(id)
		h.Info("播放事件已记录", "event", e.Name, "user", data.UserName, "item", data.ItemName)
	}
}
func (a *application) enqueue(id string) {
	if id == "" || a.jobs == nil {
		return
	}
	a.queuedMu.Lock()
	defer a.queuedMu.Unlock()
	if a.queued[id] {
		return
	}
	select {
	case a.jobs <- id:
		a.queued[id] = true
	default:
	}
}
func (a *application) enrich(ctx context.Context, h *sdk.Host, id string, force bool) error {
	r, ok := a.store.Get(id)
	if !ok || r.ItemID == "" || r.Enriched && !force {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	item, err := h.Item(ctx, r.ItemID)
	if err != nil {
		return err
	}
	if item == nil || (item.ID == "" && item.Name == "") {
		return errors.New("宿主未返回有效条目信息")
	}
	return a.store.PutMetadata(id, func(r *Record) {
		if item.ID != "" {
			r.ItemID = item.ID
		}
		if item.Name != "" {
			r.ItemName = item.Name
		}
		if item.Type != "" {
			r.ItemType = item.Type
		}
		if item.SeriesID != "" {
			r.SeriesID = item.SeriesID
		}
		if item.SeriesName != "" {
			r.SeriesName = item.SeriesName
		}
		if item.Year > 0 {
			r.Year = item.Year
		}
		r.SeasonNumber = item.SeasonNumber
		r.EpisodeNumber = item.EpisodeNumber
		if item.RuntimeTicks > 0 {
			r.RuntimeTicks = item.RuntimeTicks
		}
		r.Enriched = true
		if r.EndedAt != nil && r.RuntimeTicks > 0 && float64(r.PositionTicks)/float64(r.RuntimeTicks) >= float64(cfgInt(h.Config(), "completionPercent", 90))/100 {
			r.Completed = true
		}
	})
}
func (a *application) worker(h *sdk.Host) {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case id := <-a.jobs:
			if err := a.enrich(a.ctx, h, id, false); err != nil && a.ctx.Err() == nil {
				h.Debug("条目信息本次未补全，可运行修复任务", "record", id, "error", err.Error())
			}
			a.queuedMu.Lock()
			delete(a.queued, id)
			a.queuedMu.Unlock()
		}
	}
}
func (a *application) flushWorker(h *sdk.Host) {
	defer a.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if err := a.store.Flush(false); err != nil {
				h.Error("播放进度保存失败", "error", err.Error())
			}
		}
	}
}
func (a *application) repair(h *sdk.Host, t *sdk.Task) (string, error) {
	if a.store == nil {
		return "", sdk.Skip("插件尚未就绪")
	}
	rows, _, _ := a.store.Snapshot()
	sortRecords(rows, "recent")
	if len(rows) > 2000 {
		rows = rows[:2000]
	}
	if len(rows) == 0 {
		return "", sdk.Skip("没有历史记录")
	}
	success, failed := 0, 0
	for i, r := range rows {
		if err := t.Context.Err(); err != nil {
			return "", err
		}
		if r.ItemID != "" {
			if err := a.enrich(t.Context, h, r.ID, true); err != nil {
				failed++
			} else {
				success++
			}
		}
		t.Progress(float64(i+1) * 100 / float64(len(rows)))
	}
	return fmt.Sprintf("修复最近 %d 条中的 %d 条，%d 条不可用；未改动宿主媒体库", len(rows), success, failed), nil
}
