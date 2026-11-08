package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// 同名用户就是同一位用户：一次事件带 UserID、一次不带，应归并为一位，
// 而不是按 ID 拆成两个用户。
func TestSameNameUsersMerged(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	events := []PlaybackRecord{
		makeRecord("s-a", "u9", "张三", "1", "电影A", "Movie", base),
		makeRecord("s-b", "", "张三", "2", "电影B", "Movie", base.Add(time.Hour)),
	}
	for _, rec := range events {
		if _, _, err := s.Start(rec); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Stop(stopRecord(rec, rec.StartedAt.Add(10*time.Minute), 100, 1000), 90); err != nil {
			t.Fatal(err)
		}
	}
	users := s.Users()
	if len(users) != 1 || users[0].Name != "张三" || users[0].PlayCount != 2 {
		t.Fatalf("同名用户应合并为一位：%+v", users)
	}
}

// 旧版数据文件里按用户 ID 归组的条目（key=id:xxx）在加载后应迁移合并到按用户名的键下，
// 不会在用户列表里重复出现，且补齐 ID 信息。
func TestLegacyUserIDKeysMigratedOnLoad(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	rec := makeRecord("s-legacy", "u9", "张三", "1", "旧电影", "Movie", base)
	if _, _, err := s.Start(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stop(stopRecord(rec, rec.StartedAt.Add(5*time.Minute), 100, 1000), 90); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// 追加一条旧版格式的用户条目（按 ID 归组）。
	op := diskOperation{Op: "user", User: &KnownUser{Key: "id:u9", ID: "u9", Name: "张三", LastSeen: base}}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(op)
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	users := reopened.Users()
	if len(users) != 1 || users[0].Name != "张三" {
		t.Fatalf("旧 ID 键用户应并入同名用户：%+v", users)
	}
	if users[0].ID != "u9" {
		t.Fatalf("迁移时应保留旧条目的用户 ID：%+v", users[0])
	}
	if reopened.Count() != 1 {
		t.Fatalf("播放记录数应保持 1：%d", reopened.Count())
	}
}
