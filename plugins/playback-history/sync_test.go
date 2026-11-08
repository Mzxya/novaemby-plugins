package main

import (
	"testing"
	"time"
)

// 写盘节流后数据仍完整：同一时间窗内多次写入只做一次 fsync，
// Close 强制把剩余增量落盘；重新打开应恢复全部记录。
func TestSyncThrottleKeepsDataIntact(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }

	var id string
	for i := 0; i < 5; i++ {
		rec := makeRecord("s-throttle", "u1", "张三", "1", "节流电影", "Movie", base)
		var err error
		id, _, err = s.Start(rec)
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.Count() != 1 {
		t.Fatalf("同一会话应合并为一条记录：%d", s.Count())
	}
	if !s.dirty {
		t.Fatal("窗口内的合并写入应留下未落盘增量，等待节流同步")
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
		t.Fatalf("节流窗口内的写入在重启后丢失：%d", reopened.Count())
	}
	got, ok := reopened.Get(id)
	if !ok || got.ItemName != "节流电影" {
		t.Fatalf("节流写入的记录内容不正确：%+v", got)
	}
}

// 记录完结（Stop）时强制落盘：即使处于节流窗口内，dirty 也应被清掉。
func TestStopForcesSync(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	rec := makeRecord("s-force", "u1", "张三", "2", "落盘电影", "Movie", base)
	if _, _, err := s.Start(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stop(stopRecord(rec, base.Add(time.Minute), 60*ticksPerSecond, 100*ticksPerSecond), 90); err != nil {
		t.Fatal(err)
	}
	if s.dirty {
		t.Fatal("Stop 后应已强制落盘，不再有未落盘增量")
	}
}
