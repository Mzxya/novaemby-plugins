package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func makeRecord(session, userID, userName, itemID, name, itemType string, at time.Time) PlaybackRecord {
	return PlaybackRecord{
		SessionID: session, UserID: userID, UserName: userName, ItemID: itemID, ItemName: name,
		ItemType: itemType, StartedAt: at, LastEventAt: at, DeviceName: "客厅电视", Client: "Android TV",
	}
}

func stopRecord(start PlaybackRecord, at time.Time, position, runtime int64) PlaybackRecord {
	rec := start
	rec.LastEventAt = at
	rec.PositionTicks = position
	rec.RuntimeTicks = runtime
	return rec
}

func TestPlaybackLifecycleAndPersistence(t *testing.T) {
	s := openTestStore(t)
	start := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return start.Add(time.Hour) }
	rec := makeRecord("session-1", "u1", "张三", "100", "测试电影", "Movie", start)
	id, created, err := s.Start(rec)
	if err != nil || !created || id == "" {
		t.Fatalf("Start() = %q, %v, %v", id, created, err)
	}
	if _, created, err = s.Start(rec); err != nil || created {
		t.Fatalf("重复开始应合并同一会话：created=%v err=%v", created, err)
	}
	if s.Count() != 1 {
		t.Fatalf("重复开始产生了新记录：%d", s.Count())
	}
	if err := s.Enrich(id, "100", &sdk.Item{ID: "100", Name: "测试电影", Type: "Movie", Year: 2026, RuntimeTicks: 7200 * ticksPerSecond}, 90); err != nil {
		t.Fatal(err)
	}
	end := start.Add(90 * time.Minute)
	stopped := stopRecord(rec, end, 6840*ticksPerSecond, 7200*ticksPerSecond)
	if _, err := s.Stop(stopped, 90); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(id)
	if !ok || got.EndedAt == nil || !got.Completed || got.DurationSeconds != 5400 || got.ProgressPercent() != 95 {
		t.Fatalf("停止后的记录不正确：%+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Count() != 1 {
		t.Fatalf("重新打开后记录数 = %d", reopened.Count())
	}
	got, _ = reopened.Get(id)
	if got.ItemName != "测试电影" || !got.Completed || got.Year != 2026 {
		t.Fatalf("持久化内容不正确：%+v", got)
	}
}

func TestStopWithoutStartCreatesRecord(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 15, 0, 0, 0, time.UTC)
	rec := makeRecord("session-mid", "u2", "李四", "200", "中途播放", "Movie", at)
	rec = stopRecord(rec, at, 1800*ticksPerSecond, 3600*ticksPerSecond)
	id, err := s.Stop(rec, 90)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(id)
	if !ok || got.DurationSeconds != 1800 || got.ProgressPercent() != 50 || got.StartPositionTicks != 0 {
		t.Fatalf("中途停止记录不正确：%+v", got)
	}
}

func TestQueryUsersAndPagination(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	inputs := []PlaybackRecord{
		makeRecord("s1", "u1", "张三", "1", "流浪地球", "Movie", base),
		makeRecord("s2", "u1", "张三", "2", "测试剧集 第一集", "Episode", base.Add(time.Hour)),
		makeRecord("s3", "u2", "李四", "3", "另一部电影", "Movie", base.Add(2*time.Hour)),
	}
	for _, rec := range inputs {
		if _, _, err := s.Start(rec); err != nil {
			t.Fatal(err)
		}
		stopped := stopRecord(rec, rec.StartedAt.Add(30*time.Minute), 100, 1000)
		if _, err := s.Stop(stopped, 90); err != nil {
			t.Fatal(err)
		}
	}
	result := s.Query(Query{UserKey: "name:张三", Page: 1, PageSize: 1})
	if result.Total != 2 || result.Pages != 2 || len(result.Records) != 1 || result.Records[0].ItemID != "2" {
		t.Fatalf("用户筛选或分页不正确：%+v", result)
	}
	result = s.Query(Query{Search: "流浪", Page: 1, PageSize: 30})
	if result.Total != 1 || result.Stats.UniqueItems != 1 {
		t.Fatalf("关键词筛选不正确：%+v", result)
	}
	result = s.Query(Query{ItemType: "Movie", From: base.Add(90 * time.Minute), Page: 1, PageSize: 30})
	if result.Total != 1 || result.Records[0].UserName != "李四" {
		t.Fatalf("类型或日期筛选不正确：%+v", result)
	}
	users := s.Users()
	if len(users) != 2 || users[0].Name != "李四" || users[0].PlayCount != 1 || users[1].PlayCount != 2 {
		t.Fatalf("用户汇总不正确：%+v", users)
	}
}

func TestDeleteClearAndCleanup(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i, rec := range []PlaybackRecord{
		makeRecord("old", "u1", "张三", "1", "旧电影", "Movie", now.AddDate(-1, 0, -5)),
		makeRecord("recent", "u1", "张三", "2", "新电影", "Movie", now.AddDate(0, 0, -2)),
		makeRecord("other", "u2", "李四", "3", "其他电影", "Movie", now),
	} {
		if _, _, err := s.Start(rec); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := s.Stop(stopRecord(rec, rec.StartedAt.Add(time.Minute), 100, 100), 90); err != nil {
				t.Fatal(err)
			}
		}
	}
	removed, err := s.Cleanup(365, 1000)
	if err != nil || removed != 1 || s.Count() != 2 {
		t.Fatalf("过期清理 = %d, %v，剩余 %d", removed, err, s.Count())
	}
	removed, err = s.Clear("name:张三", time.Time{})
	if err != nil || removed != 1 || s.Count() != 1 {
		t.Fatalf("用户清理 = %d, %v，剩余 %d", removed, err, s.Count())
	}
	var id string
	for key := range s.records {
		id = key
	}
	deleted, err := s.Delete(id)
	if err != nil || !deleted || s.Count() != 0 {
		t.Fatalf("单条删除 = %v, %v", deleted, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Count() != 0 {
		t.Fatalf("清理后的数据在重启后恢复：%d", reopened.Count())
	}
}

func TestFlexibleID(t *testing.T) {
	var event playbackEvent
	if err := json.Unmarshal([]byte(`{"Session":{"ID":"abc","UserID":123,"ItemID":456},"ItemName":"测试"}`), &event); err != nil {
		t.Fatal(err)
	}
	if event.sessionID() != "abc" || event.Session.UserID.String() != "123" || event.itemID() != "456" {
		t.Fatalf("ID 解析不正确：%+v", event)
	}
}
