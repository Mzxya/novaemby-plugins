package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const ticksPerSecond int64 = 10000000
const progressInterval = 30 * time.Second

type Record struct {
	EventItemID   string     `json:"eventItemId,omitempty"`
	Partial       bool       `json:"partial,omitempty"`
	ID            string     `json:"id"`
	SessionID     string     `json:"sessionId,omitempty"`
	UserID        string     `json:"userId,omitempty"`
	UserName      string     `json:"userName"`
	ItemID        string     `json:"itemId"`
	ItemName      string     `json:"itemName"`
	ItemType      string     `json:"itemType"`
	SeriesID      string     `json:"seriesId,omitempty"`
	SeriesName    string     `json:"seriesName,omitempty"`
	SeasonNumber  *int       `json:"seasonNumber,omitempty"`
	EpisodeNumber *int       `json:"episodeNumber,omitempty"`
	Year          int        `json:"year,omitempty"`
	StartedAt     time.Time  `json:"startedAt"`
	LastEventAt   time.Time  `json:"lastEventAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	PositionTicks int64      `json:"positionTicks"`
	RuntimeTicks  int64      `json:"runtimeTicks,omitempty"`
	Completed     bool       `json:"completed"`
	DeviceName    string     `json:"deviceName,omitempty"`
	DeviceID      string     `json:"deviceId,omitempty"`
	Client        string     `json:"client,omitempty"`
	PlayMethod    string     `json:"playMethod,omitempty"`
	Paused        bool       `json:"paused,omitempty"`
	Enriched      bool       `json:"enriched,omitempty"`
	// Computed for responses, never trusted when loading older JSONL entries.
	Title           string `json:"title,omitempty"`
	Status          string `json:"status,omitempty"`
	DurationSeconds int64  `json:"durationSeconds"`
}

type EventData struct {
	Record
	HasPosition bool
	HasPaused   bool
	Finished    bool
	SkipNew     bool
}

func userKey(id, name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return "name:" + strings.ToLower(name)
	}
	return "id:" + id
}
func (r Record) key() string { return userKey(r.UserID, r.UserName) }
func (r Record) title() string {
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
	if r.ItemName != "" {
		return r.ItemName
	}
	return "未知内容"
}
func (r Record) view(now time.Time) Record {
	r.Title = r.title()
	end := r.LastEventAt
	r.Status = "stale"
	if r.EndedAt != nil {
		end = *r.EndedAt
		r.Status = "ended"
	} else if r.Paused {
		r.Status = "paused"
	} else if now.Sub(r.LastEventAt) <= 2*time.Minute {
		r.Status = "playing"
	}
	r.DurationSeconds = max(0, int64(end.Sub(r.StartedAt)/time.Second))
	return r
}
func text(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	}
	return ""
}
func integer(v any) int64         { n, _ := strconv.ParseInt(text(v), 10, 64); return max(0, n) }
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func pick(m map[string]any, names ...string) any {
	for _, n := range names {
		if v, ok := m[n]; ok {
			return v
		}
	}
	return nil
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func boolean(v any) bool { b, _ := v.(bool); return b }

// Only the documented Session structure is accepted.
// Unknown IDs are not inferred from arbitrary numbers, names, or timestamps.
func decodePlayback(raw []byte, at time.Time) (EventData, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return EventData{}, err
	}
	s := object(root["Session"])
	var e EventData
	if s == nil {
		return EventData{}, errors.New("播放事件缺少标准 Session 结构，请升级 NovaEmby")
	}
	e.SessionID = text(pick(s, "PlaySessionID", "PlaySessionId", "SessionID", "SessionId", "ID", "Id"))
	e.UserID = text(pick(s, "UserID", "UserId"))
	e.UserName = text(s["UserName"])
	e.ItemID = first(text(pick(root, "ItemID", "ItemId")), text(pick(s, "ItemID", "ItemId")))
	e.ItemName = text(root["ItemName"])
	e.ItemType = text(root["ItemType"])
	e.DeviceName = text(s["DeviceName"])
	e.DeviceID = text(pick(s, "DeviceID", "DeviceId"))
	e.Client = text(s["Client"])
	e.PlayMethod = text(s["PlayMethod"])
	e.PositionTicks = integer(s["PositionTicks"])
	_, e.HasPosition = s["PositionTicks"]
	e.RuntimeTicks = integer(s["RuntimeTicks"])
	if e.RuntimeTicks == 0 {
		e.RuntimeTicks = integer(root["RuntimeTicks"])
	}
	e.Paused, e.HasPaused = s["IsPaused"].(bool)
	e.Finished = boolean(root["PlayedToCompletion"]) || boolean(s["PlayedToCompletion"])
	if e.SessionID == "" && (e.ItemID == "" || (e.UserID == "" && e.UserName == "")) {
		return e, errors.New("播放事件缺少可关联的会话标识")
	}
	if at.IsZero() {
		at = time.Now()
	}
	e.StartedAt = at.UTC()
	e.LastEventAt = at.UTC()
	return e, nil
}

func resolveZone(name string) (*time.Location, bool) {
	if name == "" {
		return time.UTC, false
	}
	z, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, false
	}
	return z, true
}
