package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Latest raw feedback; account/device identifiers are anonymized. Field names,
// media ID, title, positions, runtime and timestamps retain the supplied values.
const currentPlayback = `{"IitkoSG":{"UPwHQQxhi":"user-test","J8VWLzj":"device-test","VsLsiqKa8fCX":"tls","I2OacnY0_Q":"device-session-test","UpksZ80r":"Apple TV","ESj6ExD":"Infuse-Direct","C5STCH":"8.5.9","MCU3o9oQz":20701,"F0Oqaz6KpkFL":"20701","HLLdFN9":"b688b3d0b898de837583ff06f9c6fd13","BxlCZoT":1197600024,"AaBTFESMF":8400000000,"YRFBd_":false,"Zg7eUbua8upc":1,"O6Ft20w":-1,"XSbPRXVJE":"2026-11-12T10:31:48.595172265+08:00","WAO1mDeb":"2026-11-12T10:33:48.75984883+08:00"},"NP7awI":"Episode","Nd3qBGBfh6":"猪猪侠之恐龙日记.2019.S01E26.第26集.2160p.WEB-DL.H.265","BLq_NRPB":"2026-11-12T10:33:48.75984883+08:00"}`

func currentSample(t *testing.T, timestamp string, position int64, paused bool) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(currentPlayback))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil { t.Fatal(err) }
	session := root["IitkoSG"].(map[string]any)
	session["BxlCZoT"] = position
	session["YRFBd_"] = paused
	root["BLq_NRPB"] = timestamp
	data, err := json.Marshal(root)
	if err != nil { t.Fatal(err) }
	return data
}

func TestCurrentMappingUsesCorrectIDsTicksAndTime(t *testing.T) {
	at, err := time.Parse(time.RFC3339Nano, "2026-11-12T10:33:48.75984883+08:00")
	if err != nil { t.Fatal(err) }
	e, err := decodePlayback([]byte(currentPlayback), at.Truncate(time.Second))
	if err != nil { t.Fatal(err) }
	for name, pair := range map[string][2]string{
		"session": {e.SessionID, "b688b3d0b898de837583ff06f9c6fd13"},
		"user": {e.UserName, "tls"}, "userID": {e.UserID, "user-test"},
		"item": {e.ItemID, "20701"}, "type": {e.ItemType, "Episode"},
		"title": {e.ItemName, "猪猪侠之恐龙日记.2019.S01E26.第26集.2160p.WEB-DL.H.265"},
		"device": {e.DeviceName, "Apple TV"}, "deviceID": {e.DeviceID, "device-test"}, "client": {e.Client, "Infuse-Direct"},
	} { if pair[0] != pair[1] { t.Fatalf("%s = %q, want %q", name, pair[0], pair[1]) } }
	if e.PositionTicks != 1197600024 || e.RuntimeTicks != 8400000000 || !e.HasPosition { t.Fatal("进度或总时长解析错误", e) }
	if e.Paused || !e.HasPaused || e.Finished { t.Fatal("暂停或完成标志错误", e) }
	if !e.LastEventAt.Equal(at) || !e.StartedAt.Equal(at) { t.Fatal("事件时间被会话开始或最后活跃时间替代", e) }
}

func TestCurrentProgressSequenceUpdatesOneExistingPlay(t *testing.T) {
	s := testStore(t)
	// The supplied samples are progress only. The start below is synthesized to
	// test correlation; it does not imply the plugin can replay a missing start.
	startAt, err := time.Parse(time.RFC3339Nano, "2026-11-12T10:31:48.595172265+08:00")
	if err != nil { t.Fatal(err) }
	start, err := decodePlayback(currentSample(t, startAt.Format(time.RFC3339Nano), 0, false), startAt.Truncate(time.Second))
	if err != nil { t.Fatal(err) }
	id := apply(t, s, "playback.started", start)
	for _, sample := range []struct { timestamp string; position int64 }{
		{"2026-11-12T10:33:08.713594394+08:00", 797199976},
		{"2026-11-12T10:33:18.725521454+08:00", 897599978},
		{"2026-11-12T10:33:28.740145684+08:00", 997199987},
		{"2026-11-12T10:33:38.748391698+08:00", 1097200022},
		{"2026-11-12T10:33:48.75984883+08:00", 1197600024},
	} {
		at, err := time.Parse(time.RFC3339Nano, sample.timestamp)
		if err != nil { t.Fatal(err) }
		e, err := decodePlayback(currentSample(t, sample.timestamp, sample.position, false), at.Truncate(time.Second))
		if err != nil { t.Fatal(err) }
		if got := apply(t, s, "playback.progress", e); got != id { t.Fatal("连续进度没有合并现有会话") }
	}
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 || rows[0].PositionTicks != 1197600024 || rows[0].RuntimeTicks != 8400000000 || rows[0].EndedAt != nil || rows[0].Completed { t.Fatalf("进度合并错误: %+v", rows) }
	if rows[0].DurationSeconds != 120 { t.Fatal("会话经过时长不应被进度位置替代", rows[0].DurationSeconds) }
}

func TestCurrentProgressDoesNotInventMissingStart(t *testing.T) {
	s := testStore(t)
	e, err := decodePlayback([]byte(currentPlayback), time.Time{})
	if err != nil { t.Fatal(err) }
	if id := apply(t, s, "playback.progress", e); id != "" { t.Fatal("没有开始事件不应伪造播放记录") }
	rows, _, _ := s.Snapshot()
	if len(rows) != 0 { t.Fatal("进度事件创建了额外播放次数") }
}

func TestCurrentPauseResumePresenceAndTimestampFallback(t *testing.T) {
	s := testStore(t)
	at, err := time.Parse(time.RFC3339Nano, "2026-11-12T10:33:48.75984883+08:00")
	if err != nil { t.Fatal(err) }
	start, err := decodePlayback(currentSample(t, at.Format(time.RFC3339Nano), 0, false), at)
	if err != nil { t.Fatal(err) }
	id := apply(t, s, "playback.started", start)
	for _, paused := range []bool{true, false} {
		at = at.Add(time.Second)
		e, err := decodePlayback(currentSample(t, at.Format(time.RFC3339Nano), 100, paused), at.Truncate(time.Second))
		if err != nil { t.Fatal(err) }
		apply(t, s, "playback.paused", e)
		r, _ := s.Get(id)
		if r.Paused != paused { t.Fatal("暂停/恢复未遵循明确标志") }
	}
	conflict := at.Add(time.Hour)
	e, err := decodePlayback([]byte(currentPlayback), conflict)
	if err != nil || !e.LastEventAt.Equal(conflict) { t.Fatal("冲突时间应使用 SDK 时间", err) }
	raw := strings.Replace(currentPlayback, `"F0Oqaz6KpkFL":"20701",`, "", 1)
	e, err = decodePlayback([]byte(raw), at)
	if err != nil || e.ItemID != "20701" { t.Fatal("数值条目 ID 回退失败", err) }
	raw = strings.Replace(currentPlayback, `"YRFBd_":false,`, "", 1)
	e, err = decodePlayback([]byte(raw), at)
	if err != nil || e.HasPaused { t.Fatal("缺失暂停标志不应当成明确 false", err) }
}
