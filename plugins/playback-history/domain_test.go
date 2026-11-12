package main

import (
	"net/url"
	"testing"
	"time"
)

func TestDocumentedEventMapping(t *testing.T) {
	base := time.Now().UTC()
	for _, raw := range []string{
		`{"Session":{"ID":"s1","UserID":"u1","UserName":"Alice","ItemID":100,"PositionTicks":500,"RuntimeTicks":1000},"ItemName":"电影","ItemType":"Movie"}`,
		`{"Session":{"PlaySessionId":"s1","UserId":"u1","UserName":"Alice","ItemId":100,"PositionTicks":500,"RuntimeTicks":1000},"ItemName":"电影","ItemType":"Movie"}`,
	} {
		e, err := decodePlayback([]byte(raw), base)
		if err != nil || e.SessionID != "s1" || e.ItemID != "100" || e.UserName != "Alice" || e.PositionTicks != 500 || e.RuntimeTicks != 1000 || !e.HasPosition {
			t.Fatalf("解析错误: %+v %v", e, err)
		}
	}
	if _, err := decodePlayback([]byte(`{"random":{"name":"Alice","number":100}}`), base); err == nil {
		t.Fatal("未知结构不应猜测媒体 ID")
	}
	e, err := decodePlayback([]byte(`{"Session":{"ID":"s1"}}`), base)
	if err != nil || e.HasPosition {
		t.Fatal("无位置的事件不应将位置重置为0")
	}
}
func TestZoneOverviewAndFiltering(t *testing.T) {
	zone, valid := resolveZone("Asia/Shanghai")
	if !valid {
		t.Fatal("内置时区不可用")
	}
	now := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	r := event(now.Add(-time.Hour), "s1", "100").Record
	r.ID = "r1"
	r = r.view(now)
	out := makeOverview([]Record{r}, now, zone, "", 7)
	if out.To != "2026-11-12" || out.Today.Plays != 1 || out.Daily[6].Plays != 1 {
		t.Fatalf("跨日统计错误: %+v", out)
	}
	f, err := parseFilter(url.Values{"from": {"2026-11-12"}, "to": {"2026-11-12"}}, zone)
	if err != nil || len(filterRecords([]Record{r}, f)) != 1 {
		t.Fatal("筛选时区不一致", err)
	}
	if z, valid := resolveZone("not/a/zone"); valid || z != time.UTC {
		t.Fatal("非法时区未回退UTC")
	}
}
func TestDSTDateBoundaries(t *testing.T) {
	zone, _ := resolveZone("America/New_York")
	for _, c := range []struct {
		day   string
		hours time.Duration
	}{{"2026-03-08", 23}, {"2026-11-01", 25}} {
		from, err := dateBoundary(c.day, false, zone)
		if err != nil {
			t.Fatal(err)
		}
		to, err := dateBoundary(c.day, true, zone)
		if err != nil {
			t.Fatal(err)
		}
		if to.Sub(from)+time.Nanosecond != c.hours*time.Hour {
			t.Fatal("夏令时自然日边界错误", c.day)
		}
	}
}
func TestFilteringGroupingAndTies(t *testing.T) {
	base := time.Now().UTC()
	a := event(base, "s1", "100").Record
	a.ID = "1"
	a.DurationSeconds = 60
	b := a
	b.ID = "2"
	b.UserName = "Bob"
	b.DurationSeconds = 120
	rows := []Record{b, a}
	sortRecords(rows, "recent")
	if rows[0].ID != "1" {
		t.Fatal("并列排序不稳定")
	}
	group := groupContents(rows, "count")
	if len(group) != 1 || group[0].Plays != 2 || group[0].Seconds != 180 {
		t.Fatal("内容聚合错误")
	}
	filtered := filterRecords(rows, Filter{User: "name:alice", Search: "电视", Status: "open"})
	if len(filtered) != 1 {
		t.Fatal("多个筛选未组合")
	}
	if totals(rows).Contents != 1 {
		t.Fatal("重复内容未合并")
	}
	for _, q := range []url.Values{{"from": {"2026-11-12"}, "to": {"2026-11-11"}}, {"page": {"-1"}}, {"mode": {"bogus"}}, {"sort": {"count"}}, {"status": {"unknown"}}} {
		if _, err := parseFilter(q, time.UTC); err == nil {
			t.Fatal("接受了无效筛选", q)
		}
	}
}

func TestStandardPauseStateAndCompletion(t *testing.T) {
	at := time.Now().UTC()
	for _, tc := range []struct {
		raw                         string
		paused, hasPaused, finished bool
	}{
		{`{"Session":{"ID":"s1","IsPaused":true}}`, true, true, false},
		{`{"Session":{"ID":"s1","IsPaused":false}}`, false, true, false},
		{`{"Session":{"ID":"s1"}}`, false, false, false},
		{`{"Session":{"ID":"s1","PlayedToCompletion":true}}`, false, false, true},
		{`{"Session":{"ID":"s1"},"PlayedToCompletion":true}`, false, false, true},
	} {
		e, err := decodePlayback([]byte(tc.raw), at)
		if err != nil || e.Paused != tc.paused || e.HasPaused != tc.hasPaused || e.Finished != tc.finished {
			t.Fatalf("standard field parsing: %+v %v", e, err)
		}
	}
}
