package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

type application struct {
	store      *Store
	enrichJobs chan string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	enrichMu   sync.Mutex
	enrichBad  map[string]bool // 已被宿主判定为无效条目 ID 的候选，避免重复请求和刷日志
}

func (a *application) enrichRejected(id string) bool {
	a.enrichMu.Lock()
	defer a.enrichMu.Unlock()
	return a.enrichBad[id]
}

func (a *application) rejectEnrich(id string) {
	a.enrichMu.Lock()
	defer a.enrichMu.Unlock()
	// 有界缓存，避免长时间运行时无效候选 ID 无限累积。
	if len(a.enrichBad) >= 1024 {
		a.enrichBad = nil
	}
	if a.enrichBad == nil {
		a.enrichBad = map[string]bool{}
	}
	a.enrichBad[id] = true
}

// 配置缺省值与 manifest.json 的 default 保持一致：
// NovaEmby 下发的配置可能缺少某些键，此时按默认值处理而不是当作 false/0。
const (
	defaultRecordMovies      = true
	defaultRecordEpisodes    = true
	defaultRecordOther       = false
	defaultRetentionDays     = 365
	defaultMaxRecords        = 50000
	defaultCompletionPercent = 90
	defaultVerboseEventLog   = false
)

func configBool(cfg sdk.Config, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func configInt(cfg sdk.Config, key string, def int64) int64 {
	if v, ok := cfg[key]; ok {
		switch t := v.(type) {
		case int64:
			return t
		case float64:
			return int64(t)
		case int:
			return int64(t)
		}
	}
	return def
}

func main() {
	a := &application{}
	sdk.Run(&sdk.Plugin{
		OnInit: func(h *sdk.Host) error {
			if err := validateConfig(h.Config()); err != nil {
				return err
			}
			st, err := OpenStore(filepath.Join(h.DataDir(), "history.jsonl"))
			if err != nil {
				return fmt.Errorf("打开播放历史数据：%w", err)
			}
			a.store = st
			if err := st.SetTimeZone(h.TimeZone()); err != nil {
				h.Warn("宿主时区无效，使用容器本地时区", "timeZone", h.TimeZone(), "error", err.Error())
			}
			a.enrichJobs = make(chan string, 256)
			a.ctx, a.cancel = context.WithCancel(context.Background())
			a.wg.Add(1)
			go a.enrichWorker(h)
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-a.ctx.Done():
						return
					case <-ticker.C:
						if err := st.FlushProgress(); err != nil {
							h.Warn("保存播放进度失败", "error", err.Error())
						}
					}
				}
			}()
			removed, err := st.Cleanup(int(configInt(h.Config(), "retentionDays", defaultRetentionDays)), int(configInt(h.Config(), "maxRecords", defaultMaxRecords)))
			if err != nil {
				h.Warn("启动时清理历史记录失败", "error", err.Error())
			} else if removed > 0 {
				h.Info("启动时清理了过期播放记录", "records", removed)
			}
			h.Info("历史播放记录插件已启动", "records", st.Count(), "page", "/history")
			return nil
		},
		OnConfigure: func(h *sdk.Host, cfg sdk.Config) error {
			if err := validateConfig(cfg); err != nil {
				return err
			}
			retention := configInt(cfg, "retentionDays", defaultRetentionDays)
			if a.store != nil {
				go func() {
					if _, err := a.store.Cleanup(int(retention), int(configInt(cfg, "maxRecords", defaultMaxRecords))); err != nil {
						h.Warn("配置更新后清理历史记录失败", "error", err.Error())
					}
				}()
			}
			return nil
		},
		OnEvent: func(h *sdk.Host, ev sdk.Event) {
			a.handleEvent(h, ev)
		},
		Routes: a.routes(),
		Tasks: map[string]sdk.TaskFunc{
			"Cleanup": func(h *sdk.Host, t *sdk.Task) (string, error) {
				if a.store == nil {
					return "", sdk.Skip("数据存储尚未初始化")
				}
				t.Progress(10)
				removed, err := a.store.Cleanup(int(configInt(h.Config(), "retentionDays", defaultRetentionDays)), int(configInt(h.Config(), "maxRecords", defaultMaxRecords)))
				if err != nil {
					return "", err
				}
				t.Progress(100)
				if removed == 0 {
					return "", sdk.Skip("没有需要清理的播放记录")
				}
				return fmt.Sprintf("清理了 %d 条播放记录，剩余 %d 条", removed, a.store.Count()), nil
			},
		},
		OnShutdown: func(h *sdk.Host) {
			if a.cancel != nil {
				a.cancel()
			}
			done := make(chan struct{})
			go func() {
				a.wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
			}
			if a.store != nil {
				_ = a.store.Close()
			}
			h.Info("历史播放记录插件已停止")
		},
	})
}

func (a *application) handleEvent(h *sdk.Host, ev sdk.Event) {
	// 高频进度直接合并现有会话，不打印逐条日志、不重复请求条目信息。
	if ev.Name == "playback.progress" {
		if a.store == nil {
			return
		}
		p := parsePlaybackEvent(ev.Data)
		err := a.store.Progress(PlaybackRecord{
			SessionID: p.SessionID, UserID: p.UserID, UserName: p.UserName,
			ItemID: p.ItemID, PositionTicks: p.PositionTicks, RuntimeTicks: p.RuntimeTicks,
			LastEventAt: eventTime(ev),
		})
		if err != nil && h != nil {
			h.Warn("保存播放进度失败", "error", err.Error())
		}
		return
	}
	// 逐条运行日志默认关闭，只在诊断开关打开时输出；异常日志始终保留。
	verbose := configBool(h.Config(), "verboseEventLog", defaultVerboseEventLog)

	a.logEventFull(h, ev)
	if a.store == nil {
		h.Warn("数据存储尚未就绪，未生成播放记录")
		return
	}
	if strings.HasPrefix(ev.Name, "user.") {
		var data struct {
			UserID   FlexibleID `json:"UserID"`
			UserName string     `json:"UserName"`
		}
		_ = ev.Decode(&data)
		id, name := data.UserID.String(), strings.TrimSpace(data.UserName)
		if id == "" && name == "" { // 字段名被混淆时按值形状兜底
			shaped := parseByShape(ev.Data)
			id, name = shaped.UserID, shaped.UserName
		}
		if id == "" && name == "" {
			return
		}
		err := a.store.AddUser(KnownUser{
			Key: userKey(id, name), ID: id,
			Name: name, Deleted: ev.Name == "user.deleted", LastSeen: eventTime(ev),
		})
		if err != nil {
			h.Warn("保存用户信息失败", "error", err.Error())
		}
		return
	}

	switch ev.Name {
	case "playback.started", "playback.paused", "playback.stopped":
	default:
		if verbose {
			h.Info("该事件类型仅用于诊断，不生成播放记录", "event", ev.Name)
		}
		return
	}

	p := parsePlaybackEvent(ev.Data)
	// 关键：任何情况下都先落一条记录，不再因为字段缺失而整条丢弃。
	if strings.TrimSpace(p.UserName) == "" {
		p.UserName = "未知用户"
	}
	if strings.TrimSpace(p.ItemName) == "" {
		p.ItemName = "未知条目"
	}
	// 详细解析结果只在诊断开关打开时写到日志，避免刷屏挤掉真正有用的记录。
	if verbose {
		h.Info("事件解析结果", "event", ev.Name, "source", p.Source,
			"user", p.UserName, "userId", p.UserID, "session", p.SessionID,
			"item", p.ItemName, "itemId", p.ItemID, "type", p.ItemType,
			"device", p.DeviceName, "client", p.Client, "method", p.PlayMethod, "ip", p.RemoteAddr,
			"positionTicks", p.PositionTicks, "runtimeTicks", p.RuntimeTicks,
			"candidates", strings.Join(p.CandidateIDs, ","))
	}
	if !a.shouldRecord(h, p.ItemType) {
		if verbose {
			h.Warn("按配置跳过该类型", "event", ev.Name, "type", p.ItemType, "item", p.ItemName)
		}
		return
	}

	at := eventTime(ev)
	rec := PlaybackRecord{
		SessionID: p.SessionID, UserID: p.UserID, UserName: strings.TrimSpace(p.UserName),
		ItemID: p.ItemID, ItemIDCandidates: p.CandidateIDs, ItemName: strings.TrimSpace(p.ItemName),
		ItemType: p.ItemType, StartedAt: at, LastEventAt: at,
		PositionTicks: p.PositionTicks, StartPositionTicks: p.PositionTicks,
		RuntimeTicks: p.RuntimeTicks, DeviceName: p.DeviceName, Client: p.Client,
		PlayMethod: p.PlayMethod, RemoteAddr: p.RemoteAddr,
	}
	var id string
	var err error
	switch ev.Name {
	case "playback.started":
		id, _, err = a.store.Start(rec)
	case "playback.paused":
		id, err = a.store.Pause(rec)
	case "playback.stopped":
		rec.Completed = p.PlayedToCompletion
		id, err = a.store.Stop(rec, int(configInt(h.Config(), "completionPercent", defaultCompletionPercent)))
	}
	if err != nil {
		h.Warn("保存播放事件失败", "event", ev.Name, "error", err.Error())
		return
	}
	if id != "" && ev.Name != "playback.paused" {
		// 日常日志只保留开始/停止各一条：谁、播了什么。暂停、补全、跳过等不写日志。
		if verbose {
			h.Info("已记录播放事件", "event", ev.Name, "user", rec.UserName, "item", rec.ItemName, "source", p.Source)
		} else if action := playbackLogAction(ev.Name); action != "" {
			h.Info(action, "user", rec.UserName, "item", rec.ItemName)
		}
		a.enqueueEnrichment(id)
	}
}

func eventTime(ev sdk.Event) time.Time {
	if ev.At.IsZero() {
		return time.Now().UTC()
	}
	return ev.At.UTC()
}

func (a *application) shouldRecord(h *sdk.Host, itemType string) bool {
	cfg := h.Config()
	switch strings.ToLower(itemType) {
	case "movie":
		return configBool(cfg, "recordMovies", defaultRecordMovies)
	case "episode":
		return configBool(cfg, "recordEpisodes", defaultRecordEpisodes)
	case "":
		return true // 事件缺少类型时先记录，丰富条目信息后仍可查询。
	default:
		return configBool(cfg, "recordOther", defaultRecordOther)
	}
}

func (a *application) enqueueEnrichment(id string) {
	if a.enrichJobs == nil {
		return
	}
	select {
	case a.enrichJobs <- id:
	default: // 队列满时保留事件中的基本信息，避免阻塞播放事件。
	}
}

func (a *application) enrichWorker(h *sdk.Host) {
	defer a.wg.Done()
	for {
		select {
		case <-a.ctx.Done():
			return
		case id := <-a.enrichJobs:
			verbose := configBool(h.Config(), "verboseEventLog", defaultVerboseEventLog)
			rec, ok := a.store.Get(id)
			if !ok || rec.Enriched {
				continue // 已补全过的记录不重复请求宿主、不重复写盘
			}
			// 字段混淆时无法一眼认出哪个 ID 是条目 ID：把所有候选逐个查一遍，
			// host.item 能查到的就是真正条目 ID，并据此补全片名、类型、季集和海报。
			seen := map[string]bool{}
			candidates := make([]string, 0, len(rec.ItemIDCandidates)+1)
			for _, candidate := range append([]string{rec.ItemID}, rec.ItemIDCandidates...) {
				if candidate != "" && !seen[candidate] {
					seen[candidate] = true
					candidates = append(candidates, candidate)
				}
			}
			if len(candidates) > 3 {
				candidates = candidates[:3]
			}
			for _, candidate := range candidates {
				if a.enrichRejected(candidate) {
					continue // 宿主已判定该 ID 无效，不重复请求也不重复刷日志
				}
				ctx, cancel := context.WithTimeout(a.ctx, 4*time.Second)
				item, err := h.Item(ctx, candidate)
				cancel()
				if err != nil {
					if errors.Is(err, sdk.ErrNotFound) {
						a.rejectEnrich(candidate)
					}
					if verbose && !errors.Is(err, context.Canceled) && !errors.Is(err, sdk.ErrNotFound) {
						h.Debug("查询条目信息失败，本次跳过", "id", candidate, "error", err.Error())
					}
					continue
				}
				completion := int(configInt(h.Config(), "completionPercent", defaultCompletionPercent))
				if err := a.store.Enrich(id, candidate, item, completion); err != nil {
					h.Warn("补充播放条目信息失败", "record", id, "error", err.Error())
				} else {
					if verbose {
						h.Info("已补全媒体库条目信息", "itemId", candidate, "name", item.Name, "type", item.Type)
					}
				}
				break
			}
		}
	}
}

// validateConfig 同时约束后台下发值，不能只依赖设置页面的 min/max。
func validateConfig(cfg sdk.Config) error {
	for _, rule := range []struct {
		key           string
		def, min, max int64
	}{
		{"retentionDays", defaultRetentionDays, 0, 3650},
		{"maxRecords", defaultMaxRecords, 1000, 500000},
		{"completionPercent", defaultCompletionPercent, 50, 100},
	} {
		if raw, ok := cfg[rule.key]; ok {
			switch v := raw.(type) {
			case float64:
				if !(v >= float64(rule.min) && v <= float64(rule.max)) || v != float64(int64(v)) {
					return fmt.Errorf("%s 必须是 %d–%d 之间的整数", rule.key, rule.min, rule.max)
				}
			case int, int64:
			default:
				return fmt.Errorf("%s 必须是整数", rule.key)
			}
		}
		v := configInt(cfg, rule.key, rule.def)
		if v < rule.min || v > rule.max {
			return fmt.Errorf("%s 必须在 %d–%d 之间", rule.key, rule.min, rule.max)
		}
	}
	return nil
}
