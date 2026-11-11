package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Based on the 2026-11-11 raw feedback. User/device identifiers are anonymized;
// keys, item IDs, positions, runtimes and timestamps retain the observed structure.
const novemberPlayback = `{"YINxkXn5Ll":{"FGSkDNowvo":"user-test","TqM7bfKdgv":"device-test","P4PkCnUa2L6":"root","GgfWyZ":"device-session-test","Hy_WzZ2_qqP":"iPhone","A8MwMHz":"Infuse-Direct","IEdS7c0hBk":"8.5.9","TmILCY_xYpOi":56735,"IWlDpq61mjY":"56735","FgxoGjZ9_14":"481f4ade2eb1a8f694045cb7589d5194","E1Aqki":663663057,"PX5tWDaaxa":54600000000,"GEsBLGVxh":false,"RIP0Dz":null,"RTKrXfoq7a6":-1,"SVnvWTh":"2026-11-11T23:22:14.378065559+08:00","AY9zPvqptA4P":"2026-11-11T23:22:17.355532532+08:00"},"RxW1ToZ":"Movie","UVpRGwHrlK8b":"宿敌","W9e0V8XV5z2e":"2026-11-11T23:22:18.81916994+08:00","AqcD0DvuolOn":false,"DqGcEd":false}`

func observedSample(t *testing.T, itemID, title, kind, sessionID, timestamp string, position, runtime int64, paused bool) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(novemberPlayback)); decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil { t.Fatal(err) }
	session := root["YINxkXn5Ll"].(map[string]any)
	session["IWlDpq61mjY"] = itemID
	session["TmILCY_xYpOi"] = json.Number(itemID)
	session["FgxoGjZ9_14"] = sessionID
	session["E1Aqki"] = position; session["PX5tWDaaxa"] = runtime; session["GEsBLGVxh"] = paused
	root["UVpRGwHrlK8b"] = title; root["RxW1ToZ"] = kind; root["W9e0V8XV5z2e"] = timestamp
	// These fields are not completion indicators: don't interpret arbitrary booleans.
	root["AqcD0DvuolOn"] = true; root["DqGcEd"] = true
	data, err := json.Marshal(root); if err != nil { t.Fatal(err) }; return data
}

func TestNovemberMovieMappingUsesCorrectIDsAndTicks(t *testing.T) {
	at, err := time.Parse(time.RFC3339Nano, "2026-11-11T23:22:18.81916994+08:00"); if err != nil { t.Fatal(err) }
	e, err := decodePlayback([]byte(novemberPlayback), at.Truncate(time.Second))
	if err != nil { t.Fatal(err) }
	for name, pair := range map[string][2]string{
		"session": {e.SessionID, "481f4ade2eb1a8f694045cb7589d5194"},
		"user": {e.UserName, "root"}, "userID": {e.UserID, "user-test"},
		"item": {e.ItemID, "56735"}, "title": {e.ItemName, "宿敌"}, "type": {e.ItemType, "Movie"},
		"device": {e.DeviceName, "iPhone"}, "deviceID": {e.DeviceID, "device-test"}, "client": {e.Client, "Infuse-Direct"},
	} { if pair[0] != pair[1] { t.Fatalf("%s = %q, want %q", name, pair[0], pair[1]) } }
	if e.PositionTicks != 663663057 || e.RuntimeTicks != 54600000000 || !e.HasPosition { t.Fatal("ticks 错误", e) }
	if e.Paused || !e.HasPaused || e.Finished { t.Fatal("暂停或完成标志错误", e) }
	if !e.LastEventAt.Equal(at) { t.Fatal("没有恢复事件时间的亚秒精度", e.LastEventAt) }
}

func TestNovemberFeedbackCreatesThreePlaysAndHandlesPauseResume(t *testing.T) {
	s := testStore(t)
	cases := []struct {
		item, title, kind, session, start, pause, resume, stop string
		pausedPosition, resumedPosition, stopPosition, runtime int64
		seconds int64
	}{
		{"113955", "突如其来的野餐", "Episode", "de75a349d67c5619513479b90a2e60e7", "2026-11-11T23:21:35.973025221+08:00", "2026-11-11T23:21:42.312120297+08:00", "2026-11-11T23:21:42.563268768+08:00", "2026-11-11T23:21:45.029146954+08:00", 2345039024, 3196331933, 3209224641, 15000000000, 9},
		{"140962", "2号玩家心率警报", "Episode", "ddb2c4797d12913edbe1841784016a8f", "2026-11-11T23:21:58.798833776+08:00", "2026-11-11T23:22:03.095702823+08:00", "2026-11-11T23:22:03.272049402+08:00", "2026-11-11T23:22:04.641823064+08:00", 552709350, 1223132684, 1222199557, 26400000000, 5},
		{"56735", "宿敌", "Movie", "481f4ade2eb1a8f694045cb7589d5194", "2026-11-11T23:22:14.378067038+08:00", "2026-11-11T23:22:16.849080278+08:00", "2026-11-11T23:22:17.355532532+08:00", "2026-11-11T23:22:18.81916994+08:00", 22140460, 660909264, 663663057, 54600000000, 4},
	}
	for _, c := range cases {
		deliver := func(kind, timestamp string, position int64, paused bool) string {
			at, err := time.Parse(time.RFC3339Nano, timestamp); if err != nil { t.Fatal(err) }
			raw := observedSample(t, c.item, c.title, c.kind, c.session, timestamp, position, c.runtime, paused)
			e, err := decodePlayback(raw, at.Truncate(time.Second)); if err != nil { t.Fatal(err) }
			return apply(t, s, kind, e)
		}
		id := deliver("playback.started", c.start, 0, false)
		if got := deliver("playback.paused", c.pause, c.pausedPosition, true); got != id { t.Fatal("暂停创建新会话") }
		r, _ := s.Get(id); if !r.Paused { t.Fatal("暂停标志未生效") }
		deliver("playback.progress", c.pause, c.pausedPosition, true)
		deliver("playback.paused", c.resume, c.resumedPosition, false)
		r, _ = s.Get(id); if r.Paused { t.Fatal("paused 事件中的 false 应表示恢复播放") }
		deliver("playback.progress", c.resume, c.resumedPosition, false)
		if got := deliver("playback.stopped", c.stop, c.stopPosition, false); got != id { t.Fatal("结束创建新会话") }
		r, _ = s.Get(id)
		if r.EndedAt == nil || r.PositionTicks != c.stopPosition || r.RuntimeTicks != c.runtime || r.ItemID != c.item || r.Completed { t.Fatalf("结束记录错误: %+v", r) }
		if got := r.view(*r.EndedAt).DurationSeconds; got != c.seconds { t.Fatalf("会话秒数=%d, want %d", got, c.seconds) }
	}
	rows, _, _ := s.Snapshot()
	if len(rows) != 3 || totals(rows).Plays != 3 || totals(rows).Seconds != 18 { t.Fatalf("高频暂停/进度不应增加次数或按跳播位置累计时长: %+v", totals(rows)) }
}

func TestPauseFlagPresenceAndTimestampFallback(t *testing.T) {
	s := testStore(t); at := time.Now().UTC(); e := event(at, "s1", "100"); id := apply(t, s, "playback.started", e)
	e.LastEventAt = at.Add(time.Second); apply(t, s, "playback.paused", e)
	e.LastEventAt = at.Add(2*time.Second); apply(t, s, "playback.progress", e)
	r, _ := s.Get(id); if !r.Paused { t.Fatal("无暂停字段的进度不应擅自清除暂停状态") }
	e.LastEventAt = at.Add(3*time.Second); e.HasPaused = true; e.Paused = false; apply(t, s, "playback.paused", e)
	r, _ = s.Get(id); if r.Paused { t.Fatal("明确 false 必须恢复播放") }
	// A conflicting raw timestamp must not override the SDK's event time.
	decoded, err := decodePlayback([]byte(novemberPlayback), at)
	if err != nil || !decoded.LastEventAt.Equal(at) { t.Fatal("原文时间冲突时没有保留 SDK 时间", err) }
	// Numeric item ID remains available when its duplicate string field is absent.
	raw := strings.Replace(novemberPlayback, `"IWlDpq61mjY":"56735",`, "", 1)
	decoded, err = decodePlayback([]byte(raw), at)
	if err != nil || decoded.ItemID != "56735" { t.Fatal("数值条目 ID 回退失败", err) }
}
