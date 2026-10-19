package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ───── 元数据来源（manifest 的 metadata）─────

// MetadataRequest NovaEmby 刮削一个条目时交给插件的信息（已经合并了本地、TMDB、豆瓣的结果）。
type MetadataRequest struct {
	// Context 超时（manifest 的 metadata.timeoutSec）或刮削取消时被取消；联网请求请用它。
	Context context.Context `json:"-"`

	ID               string            `json:"id"`
	LibraryID        string            `json:"libraryId"`
	Type             string            `json:"type"` // Movie / Series / Season / Episode
	Name             string            `json:"name"`
	OriginalTitle    string            `json:"originalTitle"`
	Year             int               `json:"year"`
	PremiereDate     string            `json:"premiereDate"` // YYYY-MM-DD，空 = 未知
	Path             string            `json:"path"`
	SeriesName       string            `json:"seriesName"`        // 季、集：所属剧集
	SeriesProviderID map[string]string `json:"seriesProviderIds"` // 季、集：剧集的外部 ID
	SeasonNumber     *int              `json:"seasonNumber"`
	EpisodeNumber    *int              `json:"episodeNumber"`
	EpisodeNumberEnd *int              `json:"episodeNumberEnd"`
	ProviderIDs      map[string]string `json:"providerIds"`   // Tmdb / Imdb / Tvdb / Douban
	HasOverview      bool              `json:"hasOverview"`   // 已经有简介
	MissingImages    []string          `json:"missingImages"` // 缺少的图片类型：Primary / Backdrop / Logo
	Language         string            `json:"-"`             // 元数据语言，例如 zh-CN
	Country          string            `json:"-"`
}

// Metadata 插件提供的数据。都可以不填；NovaEmby 只用它补条目上空着的字段，已有的不会被覆盖。
type Metadata struct {
	Name            string
	OriginalTitle   string
	SortName        string
	Overview        string
	Tagline         string
	ProductionYear  int
	PremiereDate    time.Time
	EndDate         time.Time
	CommunityRating *float64 // 0–10
	CriticRating    *float64 // 0–100
	OfficialRating  string   // 分级，例如 PG-13
	RuntimeMinutes  int
	Status          string             // 剧集：Continuing / Ended
	ProviderIDs     map[string]string  // Tmdb / Imdb / Tvdb / Douban（只填条目上空着的）
	Ratings         map[string]float64 // 评分明细，键为小写字母，例如 bangumi
	Genres          []string
	Studios         []string
	Tags            []string
	Countries       []string
	Images          []Image // 只补条目缺少的类型，NovaEmby 负责下载
}

// Image 图片地址（http / https）。Type：Primary（海报）/ Backdrop（背景图）/ Logo / Thumb / Banner。
type Image struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// MetadataFunc 为一个条目提供元数据。没找到返回 nil, nil；出错返回 error（NovaEmby 记日志，照常完成刮削）。
type MetadataFunc func(h *Host, r *MetadataRequest) (*Metadata, error)

func (m *Metadata) wire() map[string]any {
	date := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	}
	return map[string]any{
		"name": m.Name, "originalTitle": m.OriginalTitle, "sortName": m.SortName, "overview": m.Overview, "tagline": m.Tagline,
		"productionYear": m.ProductionYear, "premiereDate": date(m.PremiereDate), "endDate": date(m.EndDate),
		"communityRating": m.CommunityRating, "criticRating": m.CriticRating, "officialRating": m.OfficialRating,
		"runtimeMinutes": m.RuntimeMinutes, "status": m.Status, "providerIds": m.ProviderIDs, "ratings": m.Ratings,
		"genres": m.Genres, "studios": m.Studios, "tags": m.Tags, "countries": m.Countries, "images": m.Images,
	}
}

func (rt *runtime) handleMetadata(m message) {
	if rt.p.OnMetadata == nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32601, Message: "插件没有实现 OnMetadata（manifest 声明了 metadata？）"})
		return
	}
	var p struct {
		Item     MetadataRequest `json:"item"`
		Language string          `json:"language"`
		Country  string          `json:"country"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "metadata.fetch 参数不正确"})
		return
	}
	req := p.Item
	req.Language, req.Country = p.Language, p.Country
	ctx, done := rt.track(m.ID)
	req.Context = ctx
	go func() {
		defer done()
		var res *Metadata
		err := safeErr(func() error {
			var e error
			res, e = rt.p.OnMetadata(rt.h, &req)
			return e
		})
		switch {
		case err != nil:
			rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: err.Error()})
		case res == nil:
			rt.c.reply(m.ID, nil, nil) // 没找到：result 为 null
		default:
			rt.c.reply(m.ID, res.wire(), nil)
		}
	}()
}

// ───── 计划任务（manifest 的 tasks）─────

// Task 一次运行中的计划任务。
type Task struct {
	// Context 管理员取消、超过最长运行时间、NovaEmby 停止时被取消。长任务要经常检查它。
	Context context.Context
	Key     string // manifest 中的任务 key

	runID string
	c     *conn
}

// Progress 上报进度（0–100），显示在计划任务列表里。可以不调用。
func (t *Task) Progress(percent float64) {
	t.c.notify("task.progress", map[string]any{"runId": t.runID, "percent": percent})
}

// TaskFunc 执行一次任务。返回的 message 写进运行记录；返回 sdk.Skip("原因") 表示条件不满足没有执行（记为“跳过”）。
type TaskFunc func(h *Host, t *Task) (message string, err error)

type skipError struct{ reason string }

func (e skipError) Error() string { return e.reason }

// Skip 任务条件不满足、没有执行（运行记录显示为“跳过”，不算失败）。
func Skip(reason string) error { return skipError{reason: reason} }

func (rt *runtime) handleTask(m message) {
	var p struct {
		Key   string `json:"key"`
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "task.run 参数不正确"})
		return
	}
	fn := rt.p.Tasks[p.Key]
	if fn == nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32601, Message: fmt.Sprintf("插件没有实现任务 %q（Plugin.Tasks 的键要与 manifest 的 tasks 一致）", p.Key)})
		return
	}
	ctx, done := rt.track(m.ID)
	t := &Task{Context: ctx, Key: p.Key, runID: p.RunID, c: rt.c}
	go func() {
		defer done()
		var msg string
		err := safeErr(func() error {
			var e error
			msg, e = fn(rt.h, t)
			return e
		})
		var skip skipError
		switch {
		case errors.As(err, &skip):
			rt.c.reply(m.ID, map[string]any{"message": skip.reason, "skipped": true}, nil)
		case err != nil:
			rt.h.Error("计划任务出错", "task", p.Key, "error", err.Error())
			rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: err.Error()})
		default:
			rt.c.reply(m.ID, map[string]any{"message": msg}, nil)
		}
	}()
}

// track 为一个宿主请求建立可取消的 context（宿主发 $/cancel 时取消）；done 在处理结束时调用。
func (rt *runtime) track(id *json.RawMessage) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	key := string(*id)
	rt.mu.Lock()
	rt.cancels[key] = cancel
	rt.mu.Unlock()
	return ctx, func() {
		cancel()
		rt.mu.Lock()
		delete(rt.cancels, key)
		rt.mu.Unlock()
	}
}
