package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func progressFileSize(t *testing.T, s *Store) int64 {
	t.Helper()
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestProgressUpdatesMemoryWithoutPerEventWrites(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	now := base
	s.now = func() time.Time { return now }
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	size := progressFileSize(t, s)
	for i := 1; i <= 100; i++ {
		now = base.Add(time.Duration(i) * 100 * time.Millisecond)
		rec.LastEventAt = now
		rec.PositionTicks = int64(i) * ticksPerSecond
		if err := s.Progress(rec); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(id)
	if s.Count() != 1 || got.PositionTicks != 100*ticksPerSecond || !got.LastEventAt.Equal(now) || got.ItemName != "电影" {
		t.Fatalf("进度未合并原会话：%+v", got)
	}
	if progressFileSize(t, s) != size {
		t.Fatal("高频进度不应逐条追加 JSONL")
	}
	now = base.Add(31 * time.Second)
	if err := s.FlushProgress(); err != nil {
		t.Fatal(err)
	}
	if progressFileSize(t, s) <= size || len(s.progressPending) != 0 {
		t.Fatal("达到节流间隔后应持久化最新进度")
	}
	size = progressFileSize(t, s)
	if err := s.FlushProgress(); err != nil || progressFileSize(t, s) != size {
		t.Fatal("没有新进度不应重复写盘")
	}
}

func TestProgressClosePersistsLatestAndStopDoesNotResurrect(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC()
	s.now = func() time.Time { return base }
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.LastEventAt = base.Add(time.Second)
	rec.PositionTicks = 50 * ticksPerSecond
	if err := s.Progress(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.Get(id)
	if got.PositionTicks != rec.PositionTicks || !got.LastEventAt.Equal(rec.LastEventAt) {
		t.Fatalf("关闭未保存最新进度：%+v", got)
	}
	rec.LastEventAt = base.Add(2 * time.Second)
	rec.PositionTicks = 60 * ticksPerSecond
	if err := s.Progress(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stop(stopRecord(rec, base.Add(3*time.Second), 70*ticksPerSecond, 100*ticksPerSecond), 90); err != nil {
		t.Fatal(err)
	}
	rec.LastEventAt = base.Add(4 * time.Second)
	rec.PositionTicks = 80 * ticksPerSecond
	if err := s.Progress(rec); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(id)
	if got.EndedAt == nil || got.PositionTicks != 70*ticksPerSecond || len(s.progressPending) != 0 || s.Count() != 1 {
		t.Fatalf("迟到进度改变已结束会话：%+v", got)
	}
}

func TestProgressDoesNotCrossDevicesOrAcceptOldItem(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC()
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []PlaybackRecord{
		{SessionID: "other-device", UserName: "Alice", ItemID: "100", LastEventAt: base.Add(time.Second)},
		{SessionID: "s1", ItemID: "200", LastEventAt: base.Add(time.Second)},
		{SessionID: "s1", ItemID: "100", LastEventAt: base.Add(-time.Second)},
		{LastEventAt: base.Add(time.Second)},
	} {
		invalid.PositionTicks = 90 * ticksPerSecond
		if err := s.Progress(invalid); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(id)
	if got.PositionTicks != 0 || !got.LastEventAt.Equal(base) || s.Count() != 1 {
		t.Fatalf("不匹配进度修改了记录：%+v", got)
	}
	if _, err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	rec.LastEventAt = base.Add(2 * time.Second)
	if err := s.Progress(rec); err != nil || s.Count() != 0 {
		t.Fatal("进度不应重建用户删除的记录")
	}
}

func TestProgressEventUsesExistingParserWithoutLoggingOrEnrichment(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC()
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	a := &application{store: s, enrichJobs: make(chan string, 1)}
	data := json.RawMessage(`{"Session":{"ID":"s1","ItemID":"100","PositionTicks":1230000000}}`)
	a.handleEvent(nil, sdk.Event{Name: "playback.progress", At: base.Add(time.Second), Data: data})
	got, _ := s.Get(id)
	if got.PositionTicks != 1230000000 || got.UserName != "Alice" || got.ItemName != "电影" || len(a.enrichJobs) != 0 {
		t.Fatalf("进度事件处理错误：%+v", got)
	}
}

func TestPendingProgressSurvivesRewriteButNotClear(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC()
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.LastEventAt = base.Add(time.Second)
	rec.PositionTicks = 40 * ticksPerSecond
	if err := s.Progress(rec); err != nil {
		t.Fatal(err)
	}
	other := makeRecord("other", "u2", "Bob", "200", "旧影片", "Movie", base.Add(-time.Hour))
	if _, _, err := s.Start(other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clear("name:bob", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(s.progressPending) != 0 {
		t.Fatal("重写后进度应已持久化")
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(id)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if got.PositionTicks != rec.PositionTicks {
		t.Fatal("清理其他用户时丢失了内存中的最新进度")
	}
	rec.LastEventAt = base.Add(2 * time.Second)
	if err := s.Progress(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Clear("name:alice", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Count() != 0 {
		t.Fatal("停机写盘不应恢复已清空的会话")
	}
}

func TestSessionOnlyProgressAndBackwardSeek(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC()
	rec := makeRecord("s1", "u1", "Alice", "100", "电影", "Movie", base)
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	a := &application{store: s}
	for i, position := range []int64{900000000, 200000000} {
		data, err := json.Marshal(map[string]any{"Session": map[string]any{"ID": "s1", "PositionTicks": position, "RuntimeTicks": int64(1000000000)}})
		if err != nil {
			t.Fatal(err)
		}
		a.handleEvent(nil, sdk.Event{Name: "playback.progress", At: base.Add(time.Duration(i+1) * time.Second), Data: data})
	}
	got, _ := s.Get(id)
	if got.PositionTicks != 200000000 || got.RuntimeTicks != 1000000000 || got.Completed {
		t.Fatalf("会话进度应支持向后拖动，完成判定留到停止时：%+v", got)
	}
}
