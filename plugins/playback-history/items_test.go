package main

import (
	"net/url"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func mustRecord(t *testing.T, id, user, item, name, itemType string, started time.Time, seconds int64) PlaybackRecord {
	t.Helper()
	rec := PlaybackRecord{
		ID: id, SessionID: "sess-" + id, UserName: user, ItemID: item, ItemName: name, ItemType: itemType,
		StartedAt: started.UTC(), LastEventAt: started.UTC(),
		PositionTicks: seconds * ticksPerSecond,
	}
	rec.updateDuration(started.Add(time.Duration(seconds) * time.Second))
	ended := started.Add(time.Duration(seconds) * time.Second)
	rec.EndedAt = &ended
	return rec
}

func TestItemsAggregation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir + "/history.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.Local)
	// 同一部剧播 3 次（其中一次用户名大小写不同），另一部电影播 1 次。
	if _, _, err := s.Start(mustRecord(t, "e1", "root", "100", "第1集", "Episode", base, 1800)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(mustRecord(t, "e2", "Root", "100", "第1集", "Episode", base.Add(time.Hour), 3600)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(mustRecord(t, "e3", "root", "100", "第1集", "Episode", base.Add(2*time.Hour), 900)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Start(mustRecord(t, "m1", "root", "200", "电影甲", "Movie", base.Add(30*time.Minute), 5400)); err != nil {
		t.Fatal(err)
	}
	result := s.Items(Query{SortBy: "duration", Page: 1, PageSize: 10})
	if result.Total != 2 {
		t.Fatalf("应合并为 2 个内容，实际 %d", result.Total)
	}
	if len(result.Records) != 2 {
		t.Fatalf("应返回 2 行，实际 %d", len(result.Records))
	}
	// 默认按总时长排序：电影甲 5400s > 剧集 1800+3600+900=6300s，剧集应在前面。
	if result.Records[0].Title != "第1集" || result.Records[0].TotalWatchSeconds != 6300 {
		t.Fatalf("排序或时长不正确：%+v", result.Records[0])
	}
	if result.Records[0].PlayCount != 3 || result.Records[1].PlayCount != 1 {
		t.Fatalf("播放次数归并错误：%+v", result.Records)
	}
	if result.Records[0].ItemID != "100" {
		t.Fatalf("应保留最近一次播放的条目 ID，实际 %q", result.Records[0].ItemID)
	}
	// 按次数排序时电影甲仍是 1 次，剧集 3 次，剧集在前。
	byCount := s.Items(Query{SortBy: "count", Page: 1, PageSize: 10})
	if byCount.Records[0].PlayCount != 3 {
		t.Fatalf("按次数排序错误：%+v", byCount.Records[0])
	}
	// 按最近播放排序：剧集最后一次播放在 base+2h，晚于电影的 base+30min，剧集在前。
	byRecent := s.Items(Query{SortBy: "recent", Page: 1, PageSize: 10})
	if byRecent.Records[0].Title != "第1集" {
		t.Fatalf("按最近播放排序错误：%+v", byRecent.Records[0].Title)
	}
}

func TestRecordsSortByDuration(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir + "/history.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local)
	for _, rec := range []PlaybackRecord{
		mustRecord(t, "a", "root", "1", "短片", "Movie", base, 60),
		mustRecord(t, "b", "root", "2", "长片", "Movie", base.Add(time.Hour), 7200),
		mustRecord(t, "c", "root", "3", "中片", "Movie", base.Add(2*time.Hour), 600),
	} {
		if _, _, err := s.Start(rec); err != nil {
			t.Fatal(err)
		}
	}
	result := s.Query(Query{SortBy: "duration", Page: 1, PageSize: 10})
	got := ""
	for _, rec := range result.Records {
		got += rec.ItemName
	}
	if got != "长片中片短片" {
		t.Fatalf("按时长排序错误：%s", got)
	}
	oldest := s.Query(Query{SortBy: "oldest", Page: 1, PageSize: 10})
	if oldest.Records[0].ItemName != "短片" || oldest.Records[2].ItemName != "中片" {
		t.Fatalf("按最早播放排序错误：%+v", oldest.Records)
	}
}

func TestParseQuerySort(t *testing.T) {
	cases := map[string]string{"": "recent", "recent": "recent", "desc": "recent", "asc": "oldest", "oldest": "oldest", "duration": "duration", "count": "count"}
	for raw, want := range cases {
		q, err := parseQuery(&sdk.Request{Query: url.Values{"sort": {raw}}})
		if err != nil {
			t.Fatalf("sort=%q 不应报错：%v", raw, err)
		}
		if q.SortBy != want {
			t.Fatalf("sort=%q 期望 %s，实际 %s", raw, want, q.SortBy)
		}
	}
	if _, err := parseQuery(&sdk.Request{Query: url.Values{"sort": {"bad"}}}); err == nil {
		t.Fatal("非法 sort 值应报错")
	}
}
