package main

import (
	"strings"
	"testing"
	"time"
)

// 用户实测的完整事件原文（顶层键为片名/类型，会话里带数字条目 ID 和 tick 进度）。
const mappedMovieEvent = `{"UQVNVII2":{
  "EUebnB":"8b0d1149f7b4b79611d09ced064fb55f",
  "Ma1NLTTGgLkj":"7ff081eec2b083eb7d3b8967c40cbb6d",
  "FQBAcs2Ewxg":"root",
  "Lp_fY7Tvs7":"BFD8AB74-4659-4C0B-AFB5-202500DED30F",
  "VapVfGw8id":"iPhone",
  "ZgySkQw":"Infuse-Direct",
  "Thafucf0N":"8.5.9",
  "P0aX1v":1524,
  "V3S9zRa":"1524",
  "Sk2P5N":"68224542638366c810781f94a70d496d",
  "AEKpVZufJ":947079993,
  "Hq1fULgr1q":62400000000,
  "VUWeWNJXBmau":false,
  "I5sntGG_I":1,
  "IfYIkMa9Ak":-1,
  "KwVN1_8Euh9d":"2026-11-07T18:14:05.173930981+08:00",
  "GgzQPs":"2026-11-07T18:15:35.278498953+08:00"},
  "XaNoaNs":"Movie",
  "VE9xvSKkbW4R":"黑白森林",
  "IEbFReOhlBe":"2026-11-07T18:15:39.705037143+08:00",
  "I9UU8t":false,
  "JWIeeqNkqR":false}`

const mappedEpisodeEvent = `{"UQVNVII2":{
  "EUebnB":"e6f2ae32c32e56a9cd5edd72fbb6721a",
  "Ma1NLTTGgLkj":"76be8c03dd80da2690f2de8e94b22443",
  "FQBAcs2Ewxg":"tls",
  "Lp_fY7Tvs7":"AE0A10B5-6CB5-4D30-BBB5-0586CD5BD54D",
  "VapVfGw8id":"Apple TV",
  "ZgySkQw":"Infuse-Direct",
  "Thafucf0N":"8.5.9",
  "P0aX1v":20663,
  "V3S9zRa":"20663",
  "Sk2P5N":"840c8d02283b92cc5692f5f3ac1970fc",
  "AEKpVZufJ":0,
  "Hq1fULgr1q":7800000000,
  "VUWeWNJXBmau":false,
  "I5sntGG_I":1,
  "IfYIkMa9Ak":-1,
  "KwVN1_8Euh9d":"2026-11-07T18:30:11.678865016+08:00",
  "GgzQPs":"2026-11-07T18:30:11.678865016+08:00"},
  "XaNoaNs":"Episode",
  "VE9xvSKkbW4R":"猪猪侠之恐龙日记.2019.S02E17.第17集.2160p.WEB-DL.H.265.HQ",
  "IEbFReOhlBe":"2026-11-07T18:30:11.678983673+08:00"}`

func TestParseMappedMovieEvent(t *testing.T) {
	p := parsePlaybackEvent([]byte(mappedMovieEvent))
	if p.Source != "mapped" {
		t.Fatalf("应走精确映射，实际 %s", p.Source)
	}
	for _, check := range []struct {
		name, got, want string
	}{
		{"用户名", p.UserName, "root"},
		{"用户 ID", p.UserID, "8b0d1149f7b4b79611d09ced064fb55f"},
		{"片名", p.ItemName, "黑白森林"},
		{"类型", p.ItemType, "Movie"},
		{"条目 ID", p.ItemID, "1524"},
		{"播放会话 ID", p.SessionID, "68224542638366c810781f94a70d496d"},
		{"设备", p.DeviceName, "iPhone"},
		{"客户端", p.Client, "Infuse-Direct"},
	} {
		if check.got != check.want {
			t.Errorf("%s = %q，应为 %q", check.name, check.got, check.want)
		}
	}
	// 这两个值是 100ns tick，不能再被当成秒或条目 ID。
	if p.PositionTicks != 947079993 {
		t.Errorf("播放位置 ticks = %d，应为 947079993", p.PositionTicks)
	}
	if p.RuntimeTicks != 62400000000 {
		t.Errorf("总时长 ticks = %d，应为 62400000000", p.RuntimeTicks)
	}
	if len(p.CandidateIDs) != 1 || p.CandidateIDs[0] != "1524" {
		t.Errorf("候选条目 ID 应只有数字 ID，实际 %v", p.CandidateIDs)
	}
	if p.PlayedToCompletion {
		t.Error("暂停标志不应被当成「已看完」")
	}
}

func TestParseMappedEpisodeEvent(t *testing.T) {
	p := parsePlaybackEvent([]byte(mappedEpisodeEvent))
	if p.Source != "mapped" || p.UserName != "tls" || p.ItemType != "Episode" {
		t.Fatalf("剧集事件解析错误：%+v", p)
	}
	if p.ItemID != "20663" || p.RuntimeTicks != 7800000000 || p.PositionTicks != 0 {
		t.Fatalf("剧集条目/时长/位置错误：%+v", p)
	}
	if p.SessionID != "840c8d02283b92cc5692f5f3ac1970fc" {
		t.Fatalf("会话 ID 错误：%q", p.SessionID)
	}
	if p.DeviceName != "Apple TV" {
		t.Fatalf("设备名错误：%q", p.DeviceName)
	}
}

func TestMappedMappingFallsBackWhenKeysChange(t *testing.T) {
	// 键名被换掉后应自动回退到按值形状解析，不能崩也不能返回空记录。
	changed := strings.Replace(mappedMovieEvent, obfUserName, "ZZZ_UserName", 1)
	p := parsePlaybackEvent([]byte(changed))
	if p.Source != "shape" || p.UserName == "" || p.ItemName == "" {
		t.Fatalf("键名变化后未正确回退：%+v", p)
	}
}

func recordFromParsed(p parsedEvent, at time.Time) PlaybackRecord {
	return PlaybackRecord{
		SessionID: p.SessionID, UserID: p.UserID, UserName: p.UserName,
		ItemID: p.ItemID, ItemIDCandidates: p.CandidateIDs, ItemName: p.ItemName,
		ItemType: p.ItemType, StartedAt: at, LastEventAt: at,
		PositionTicks: p.PositionTicks, StartPositionTicks: p.PositionTicks,
		RuntimeTicks: p.RuntimeTicks, DeviceName: p.DeviceName, Client: p.Client,
	}
}

func TestMappedEventProducesAccurateRecord(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 10, 14, 5, 0, time.UTC)
	start := parsePlaybackEvent([]byte(mappedMovieEvent))
	start.PositionTicks = 0
	id, created, err := s.Start(recordFromParsed(start, at))
	if err != nil || id == "" || !created {
		t.Fatalf("started 未落库：id=%q created=%v err=%v", id, created, err)
	}

	stop := parsePlaybackEvent([]byte(mappedMovieEvent))
	stopped := recordFromParsed(stop, at.Add(95*time.Second))
	stopID, err := s.Stop(stopped, 90)
	if err != nil || stopID != id {
		t.Fatalf("started/stopped 未合并：%s != %s err=%v", id, stopID, err)
	}
	got, ok := s.Get(id)
	if !ok {
		t.Fatal("记录不存在")
	}
	if got.ItemID != "1524" || got.ItemName != "黑白森林" || got.UserName != "root" {
		t.Fatalf("记录内容错误：%+v", got)
	}
	if got.DurationSeconds != 95 {
		t.Errorf("观看时长 = %d 秒，应为 95", got.DurationSeconds)
	}
	if percent := got.ProgressPercent(); percent < 1 || percent > 3 {
		t.Errorf("播放进度 = %.2f%%，应约 1.5%%（旧版会错成 24%%）", percent)
	}
	if got.Completed {
		t.Error("仅看了 1.5% 不应标记为已看完")
	}
}

func TestMappedSessionIDSplitsDifferentPlays(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	first := parsePlaybackEvent([]byte(mappedMovieEvent))
	_, _, err := s.Start(recordFromParsed(first, at))
	if err != nil {
		t.Fatal(err)
	}
	// 同一用户、同一设备 GUID，但换了影片和播放会话 ID。
	secondRaw := strings.Replace(mappedMovieEvent, "68224542638366c810781f94a70d496d", "e6104f5e78e8a74a397ec150aa24a6a6", 1)
	secondRaw = strings.Replace(secondRaw, "黑白森林", "爆裂进化！暴龙兽", 1)
	secondRaw = strings.Replace(secondRaw, `"XaNoaNs":"Movie"`, `"XaNoaNs":"Episode"`, 1)
	second := parsePlaybackEvent([]byte(secondRaw))
	_, created, err := s.Start(recordFromParsed(second, at.Add(10*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("两次播放被错误合并成一条（会话 ID 取错）")
	}
	if got := s.Query(Query{Page: 1, PageSize: 30}).Total; got != 2 {
		t.Fatalf("应有 2 条记录，实际 %d", got)
	}
}
