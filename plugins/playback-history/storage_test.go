package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "history.jsonl"), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func event(at time.Time, session, item string) EventData {
	return EventData{Record: Record{SessionID: session, UserID: "u1", UserName: "Alice", ItemID: item, ItemName: "测试电影", ItemType: "Movie", DeviceName: "电视", StartedAt: at, LastEventAt: at, RuntimeTicks: 100 * ticksPerSecond}, HasPosition: true}
}
func apply(t *testing.T, s *Store, kind string, e EventData) string {
	t.Helper()
	id, _, err := s.Apply(kind, e, 90)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func fileSize(t *testing.T, s *Store) int64 {
	t.Helper()
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
func TestLifecycleAndProgressPersistence(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	now := base
	s.now = func() time.Time { return now }
	e := event(base, "s1", "100")
	id := apply(t, s, "playback.started", e)
	size := fileSize(t, s)
	for i := 1; i <= 100; i++ {
		now = base.Add(time.Duration(i) * time.Millisecond)
		e.LastEventAt = now
		e.PositionTicks = int64(i) * ticksPerSecond
		apply(t, s, "playback.progress", e)
	}
	if fileSize(t, s) != size {
		t.Fatal("高频进度被逐条写盘")
	}
	got, _ := s.Get(id)
	if got.PositionTicks != 100*ticksPerSecond {
		t.Fatal("最新进度未进内存")
	}
	now = base.Add(31 * time.Second)
	if err := s.Flush(false); err != nil {
		t.Fatal(err)
	}
	if fileSize(t, s) <= size {
		t.Fatal("节流后没有保存进度")
	}
	e.LastEventAt = now
	e.PositionTicks = 95 * ticksPerSecond
	apply(t, s, "playback.stopped", e)
	apply(t, s, "playback.stopped", e)
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 || !rows[0].Completed || rows[0].EndedAt == nil {
		t.Fatalf("结束记录不正确: %+v", rows)
	}
	e.LastEventAt = now.Add(time.Second)
	apply(t, s, "playback.progress", e)
	got, _ = s.Get(id)
	if got.PositionTicks != 95*ticksPerSecond {
		t.Fatal("迟到进度恢复了已结束记录")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, _, _ = reopened.Snapshot()
	if len(rows) != 1 || !rows[0].Completed {
		t.Fatal("重启丢失历史")
	}
}
func TestSessionsDoNotCrossDevicesOrContent(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	one := event(base, "tv", "100")
	two := event(base, "phone", "100")
	id := apply(t, s, "playback.started", one)
	apply(t, s, "playback.started", two)
	wrong := event(base.Add(time.Second), "missing", "100")
	wrong.PositionTicks = 50
	apply(t, s, "playback.progress", wrong)
	wrong.SessionID = "tv"
	wrong.ItemID = "200"
	apply(t, s, "playback.progress", wrong)
	r, _ := s.Get(id)
	if r.PositionTicks != 0 {
		t.Fatal("其他设备或条目改动了原会话")
	}
	one.LastEventAt = base.Add(-time.Second)
	one.PositionTicks = 80
	apply(t, s, "playback.progress", one)
	r, _ = s.Get(id)
	if r.PositionTicks != 0 {
		t.Fatal("旧进度倒写")
	}
	rows, _, _ := s.Snapshot()
	if len(rows) != 2 {
		t.Fatal("进度创建了新记录")
	}
}
func TestStopWithoutStartAndExcludedContent(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	e := event(base, "s1", "100")
	e.SkipNew = true
	apply(t, s, "playback.stopped", e)
	rows, _, _ := s.Snapshot()
	if len(rows) != 0 {
		t.Fatal("已排除类型仍生成记录")
	}
	e.SkipNew = false
	apply(t, s, "playback.stopped", e)
	rows, _, _ = s.Snapshot()
	if len(rows) != 1 || !rows[0].Partial || rows[0].DurationSeconds != 0 {
		t.Fatal("缺失开始事件不应使用播放位置伪造观看时长")
	}
}
func TestCanonicalMetadataDoesNotBreakProgressAssociation(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	e := event(base, "s1", "legacy")
	id := apply(t, s, "playback.started", e)
	if err := s.PutMetadata(id, func(r *Record) { r.ItemID = "canonical"; r.ItemName = "媒体库片名"; r.Enriched = true }); err != nil {
		t.Fatal(err)
	}
	e.LastEventAt = base.Add(time.Second)
	e.PositionTicks = 70
	e.ItemName = "事件原名"
	apply(t, s, "playback.progress", e)
	r, _ := s.Get(id)
	if r.PositionTicks != 70 || r.ItemName != "媒体库片名" || r.ItemID != "canonical" {
		t.Fatalf("媒体补全破坏会话关联: %+v", r)
	}
}
func TestCloseAndRewriteKeepPendingProgress(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	e := event(base, "s1", "100")
	id := apply(t, s, "playback.started", e)
	other := event(base, "s2", "200")
	other.UserName = "Bob"
	apply(t, s, "playback.started", other)
	e.LastEventAt = base.Add(time.Second)
	e.PositionTicks = 123
	apply(t, s, "playback.progress", e)
	if _, err := s.Clear("name:bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r, ok := reopened.Get(id)
	if !ok || r.PositionTicks != 123 {
		t.Fatal("清理或关闭丢失待写进度")
	}
	if n, err := reopened.Clear(""); err != nil || n != 1 {
		t.Fatal("清空失败", n, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := OpenStore(s.path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	rows, _, _ := last.Snapshot()
	if len(rows) != 0 {
		t.Fatal("待写进度恢复了已清空记录")
	}
}
func TestLegacyLogAndDamagedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	base := time.Now().UTC()
	old := map[string]any{"op": "put", "record": map[string]any{"id": "keep-id", "sessionId": "old", "itemId": "100", "userName": "Alice", "itemName": "旧记录", "startedAt": base, "lastEventAt": base, "durationSeconds": 999999}}
	b, _ := json.Marshal(old)
	b = append(b, []byte("\n{\"op\":")...)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, ok := s.Get("keep-id")
	if !ok || r.ItemName != "旧记录" {
		t.Fatal("没有保留旧日志的记录 ID")
	}
	apply(t, s, "playback.started", event(base, "new", "200"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path, time.UTC)
	if err != nil {
		t.Fatal("损坏尾行影响后续追加", err)
	}
	defer s.Close()
	rows, _, _ := s.Snapshot()
	if len(rows) != 2 {
		t.Fatal("恢复后记录数量不正确")
	}
}
func TestMiddleCorruptionAndFailedClearLeaveData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	bad := []byte("not-json\n{}\n")
	os.WriteFile(path, bad, 0600)
	if _, err := OpenStore(path, time.UTC); err == nil {
		t.Fatal("中间损坏未报错")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(bad) {
		t.Fatal("报错修改了原日志")
	}
	s := testStore(t)
	apply(t, s, "playback.started", event(time.Now().UTC(), "s1", "100"))
	original := s.path
	s.path = filepath.Join(t.TempDir(), "missing", "history.jsonl")
	if _, err := s.Clear(""); err == nil {
		t.Fatal("模拟清理失败未报错")
	}
	s.path = original
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 {
		t.Fatal("写入失败仍删除了内存记录")
	}
}
func TestStaleSessionsDoNotAccumulateForever(t *testing.T) {
	base := time.Now().UTC()
	r := event(base, "s1", "100").Record
	if got := r.view(base.Add(24 * time.Hour)); got.DurationSeconds != 0 || got.Status != "stale" {
		t.Fatal("停止事件丢失时仍无限累加会话时长")
	}
	r.LastEventAt = base.Add(time.Minute)
	r.Paused = true
	if got := r.view(base.Add(time.Hour)); got.DurationSeconds != 60 || got.Status != "paused" {
		t.Fatal("暂停会话统计口径不正确")
	}
}
func TestIncompleteStartCanLearnItsItemWithoutDuplicatingTheSession(t *testing.T) {
	s := testStore(t)
	base := time.Now().UTC()
	start := event(base, "s1", "")
	id := apply(t, s, "playback.started", start)
	progress := event(base.Add(time.Second), "s1", "100")
	progress.PositionTicks = 123
	apply(t, s, "playback.progress", progress)
	r, _ := s.Get(id)
	if r.ItemID != "100" || r.PositionTicks != 123 {
		t.Fatal("缺失条目ID的开始事件未关联后续进度")
	}
	apply(t, s, "playback.stopped", progress)
	rows, _, _ := s.Snapshot()
	if len(rows) != 1 || rows[0].EndedAt == nil {
		t.Fatal("条目ID补充后重复创建了会话")
	}
}
