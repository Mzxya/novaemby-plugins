package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTruncatedLastLineIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	rec := makeRecord("s1", "u1", "张三", "1", "完整记录", "Movie", at)
	if _, _, err := s.Start(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"put","record":{"id":"unfinished"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	s, err = OpenStore(path)
	if err != nil {
		t.Fatalf("应能恢复最后一条不完整数据：%v", err)
	}
	if s.Count() != 1 {
		t.Fatalf("完整记录丢失：%d", s.Count())
	}
	rec2 := makeRecord("s2", "u1", "张三", "2", "恢复后记录", "Movie", at.Add(time.Hour))
	if _, _, err := s.Start(rec2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Count() != 2 {
		t.Fatalf("恢复后追加的数据无法读取：%d", s.Count())
	}
}
