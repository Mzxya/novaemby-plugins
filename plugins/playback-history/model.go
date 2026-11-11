package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ticksPerSecond = int64(10_000_000)

// FlexibleID 兼容事件中数字或字符串形式的 ID。
type FlexibleID string

func (id *FlexibleID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*id = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*id = FlexibleID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = FlexibleID(n.String())
	return nil
}

func (id FlexibleID) String() string { return string(id) }

type PlaybackRecord struct {
	ID                 string     `json:"id"`
	SessionID          string     `json:"sessionId,omitempty"`
	UserID             string     `json:"userId,omitempty"`
	UserName           string     `json:"userName"`
	ItemID             string     `json:"itemId"`
	ItemIDCandidates   []string   `json:"itemIdCandidates,omitempty"` // 事件字段混淆时，待 host.item 验证的候选 ID
	Enriched           bool       `json:"enriched,omitempty"`         // 已用 host.item 补全，避免重复请求和重复写盘
	ItemName           string     `json:"itemName"`
	ItemType           string     `json:"itemType"`
	SeriesName         string     `json:"seriesName,omitempty"`
	SeriesID           string     `json:"seriesId,omitempty"` // 剧集级条目 ID，用于单集无海报时回退取图
	SeasonNumber       *int       `json:"seasonNumber,omitempty"`
	EpisodeNumber      *int       `json:"episodeNumber,omitempty"`
	Year               int        `json:"year,omitempty"`
	StartedAt          time.Time  `json:"startedAt"`
	EndedAt            *time.Time `json:"endedAt,omitempty"`
	LastEventAt        time.Time  `json:"lastEventAt"`
	DurationSeconds    int64      `json:"durationSeconds"`
	StartPositionTicks int64      `json:"startPositionTicks,omitempty"`
	PositionTicks      int64      `json:"positionTicks"`
	RuntimeTicks       int64      `json:"runtimeTicks,omitempty"`
	Completed          bool       `json:"completed"`
	DeviceName         string     `json:"deviceName,omitempty"`
	Client             string     `json:"client,omitempty"`
	PlayMethod         string     `json:"playMethod,omitempty"`
	RemoteAddr         string     `json:"remoteAddr,omitempty"`
}

// 同名用户就是同一位用户：NovaEmby 用户名唯一，且播放事件里有时带 UserID
// 有时不带，按 ID 归组会把同一人拆成两个用户，因此用户名优先作为归组键。
func userKey(userID, userName string) string {
	name := strings.ToLower(strings.TrimSpace(userName))
	if name != "" {
		return "name:" + name
	}
	return "id:" + userID
}

func (r PlaybackRecord) UserKey() string { return userKey(r.UserID, r.UserName) }

func (r PlaybackRecord) DisplayTitle() string {
	name := r.ItemName
	if name == "" {
		name = "未知条目"
	}
	if r.SeriesName != "" {
		title := r.SeriesName
		if r.SeasonNumber != nil && r.EpisodeNumber != nil {
			title += fmt.Sprintf(" S%02dE%02d", *r.SeasonNumber, *r.EpisodeNumber)
		}
		if r.ItemName != "" && r.ItemName != r.SeriesName {
			title += " · " + r.ItemName
		}
		return title
	}
	return name
}

func (r PlaybackRecord) ProgressPercent() float64 {
	if r.RuntimeTicks <= 0 || r.PositionTicks <= 0 {
		return 0
	}
	p := float64(r.PositionTicks) * 100 / float64(r.RuntimeTicks)
	if p > 100 {
		return 100
	}
	return p
}

// applyCompletion 是唯一的「已看完」判定口径：播放位置达到总时长，
// 或达到配置阈值（默认 90%）。事件里的 PlayedToCompletion 只作为额外信号。
func (r *PlaybackRecord) applyCompletion(completionPercent int, eventSignal bool) {
	if eventSignal {
		r.Completed = true
	}
	if r.RuntimeTicks > 0 && (r.PositionTicks >= r.RuntimeTicks ||
		(completionPercent > 0 && r.ProgressPercent() >= float64(completionPercent))) {
		r.Completed = true
	}
}

// durationSecondsAt 纯计算播放秒数，不修改记录本身；
// 供统计/汇总在大结果集上免 clone 使用。
func durationSecondsAt(r *PlaybackRecord, now time.Time) int64 {
	end := now
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	if end.Before(r.StartedAt) {
		return 0
	}
	return int64(end.Sub(r.StartedAt) / time.Second)
}

func (r *PlaybackRecord) updateDuration(now time.Time) {
	r.DurationSeconds = durationSecondsAt(r, now)
}

type KnownUser struct {
	Key      string    `json:"key"`
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name"`
	Deleted  bool      `json:"deleted,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

type UserSummary struct {
	Key               string     `json:"key"`
	ID                string     `json:"id,omitempty"`
	Name              string     `json:"name"`
	Deleted           bool       `json:"deleted,omitempty"`
	PlayCount         int        `json:"playCount"`
	UniqueItems       int        `json:"uniqueItems"`
	TotalWatchSeconds int64      `json:"totalWatchSeconds"`
	LastPlayedAt      *time.Time `json:"lastPlayedAt,omitempty"`
}

type playbackEvent struct {
	Session struct {
		ID                 FlexibleID `json:"ID"`
		SessionID          FlexibleID `json:"SessionID"`
		UserID             FlexibleID `json:"UserID"`
		UserName           string     `json:"UserName"`
		DeviceName         string     `json:"DeviceName"`
		Client             string     `json:"Client"`
		ItemID             FlexibleID `json:"ItemID"`
		PositionTicks      int64      `json:"PositionTicks"`
		RuntimeTicks       int64      `json:"RuntimeTicks"`
		PlayMethod         string     `json:"PlayMethod"`
		RemoteAddr         string     `json:"RemoteAddr"`
		PlayedToCompletion bool       `json:"PlayedToCompletion"`
	} `json:"Session"`
	ItemID             FlexibleID `json:"ItemID"`
	ItemType           string     `json:"ItemType"`
	ItemName           string     `json:"ItemName"`
	RuntimeTicks       int64      `json:"RuntimeTicks"`
	PlayedToCompletion bool       `json:"PlayedToCompletion"`
}

func (e playbackEvent) sessionID() string {
	if e.Session.ID != "" {
		return e.Session.ID.String()
	}
	return e.Session.SessionID.String()
}

func (e playbackEvent) itemID() string {
	if e.Session.ItemID != "" {
		return e.Session.ItemID.String()
	}
	return e.ItemID.String()
}

func (e playbackEvent) completed() bool {
	return e.PlayedToCompletion || e.Session.PlayedToCompletion
}

type Query struct {
	UserKey  string
	Search   string
	ItemType string
	From     time.Time
	To       time.Time
	Status   string
	SortBy   string // "recent"（默认）、"oldest"、"duration"；Items 聚合视图另有 "count"
	Page     int
	PageSize int
}

// ItemSummary 是「按内容汇总」视图的一行：同一部影片/剧集单集的
// 多次播放合并后的总观看时长、次数与最近播放时间。
type ItemSummary struct {
	Key               string    `json:"key"`
	ItemID            string    `json:"itemId,omitempty"`
	SeriesID          string    `json:"seriesId,omitempty"`
	Title             string    `json:"title"`
	ItemType          string    `json:"itemType,omitempty"`
	Year              int       `json:"year,omitempty"`
	PlayCount         int       `json:"playCount"`
	TotalWatchSeconds int64     `json:"totalWatchSeconds"`
	LastPlayedAt      time.Time `json:"lastPlayedAt"`
	CompletedCount    int       `json:"completedCount"`
}

type Stats struct {
	TotalPlayCount    int   `json:"totalPlayCount"`
	UniqueItems       int   `json:"uniqueItems"`
	CompletedCount    int   `json:"completedCount"`
	ActiveCount       int   `json:"activeCount"`
	TotalWatchSeconds int64 `json:"totalWatchSeconds"`
}

type QueryResult struct {
	Records  []PlaybackRecord `json:"records"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	Pages    int              `json:"pages"`
	Stats    Stats            `json:"stats"`
}

type ItemResult struct {
	Records  []ItemSummary `json:"records"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Pages    int           `json:"pages"`
	Stats    Stats         `json:"stats"`
}

// DailyBucket 是「每日观看」面板里一天的数据（服务器本地时区）。
type DailyBucket struct {
	Date         string `json:"date"` // YYYY-MM-DD
	PlayCount    int    `json:"playCount"`
	WatchSeconds int64  `json:"watchSeconds"`
}

// HourBucket 是 0–23 每个时段的分布。
type HourBucket struct {
	Hour         int   `json:"hour"`
	PlayCount    int   `json:"playCount"`
	WatchSeconds int64 `json:"watchSeconds"`
}
