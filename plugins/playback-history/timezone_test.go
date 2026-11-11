package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func TestHostTimeZoneControlsDashboardAndDateFiltering(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetTimeZone("Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	// 容器使用 UTC 时仍应属于上海的 11 月 12 日、凌晨 0 点。
	now := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	dashboardRecord(t, s, "s1", "Alice", "1", "Movie", now.Add(-time.Hour), 60)
	got := s.Dashboard("", 7)
	if got.TimeZone != "Asia/Shanghai" || got.To != "2026-11-12" || got.Today.TotalPlayCount != 1 || got.Hours[0].PlayCount != 1 || got.Daily[6].PlayCount != 1 {
		t.Fatalf("宿主时区未用于日期/小时统计：%+v", got)
	}
	a := &application{store: s}
	for _, day := range []string{"2026-11-12", "2026-11-11"} {
		resp := a.routes()["GET /history/records"](nil, &sdk.Request{Query: url.Values{"from": {day}, "to": {day}}})
		var body struct {
			Total int `json:"total"`
		}
		if resp.Status != http.StatusOK || json.Unmarshal(resp.Body, &body) != nil {
			t.Fatalf("日期筛选请求失败：%s", resp.Body)
		}
		want := 0
		if day == "2026-11-12" {
			want = 1
		}
		if body.Total != want {
			t.Fatalf("%s 筛选数量=%d，应为%d", day, body.Total, want)
		}
	}
}

func TestDateFilterHandlesShortAndLongDSTDays(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		day   string
		hours time.Duration
	}{
		{"2026-03-08", 23}, {"2026-11-01", 25},
	} {
		from, err := parseTime(tc.day, false, location)
		if err != nil {
			t.Fatal(err)
		}
		to, err := parseTime(tc.day, true, location)
		if err != nil {
			t.Fatal(err)
		}
		if to.Sub(from)+time.Nanosecond != tc.hours*time.Hour {
			t.Fatalf("%s 日期边界不是 %d 小时：%v 至 %v", tc.day, tc.hours, from, to)
		}
		if to.In(location).Format("2006-01-02 15:04:05") != tc.day+" 23:59:59" {
			t.Fatalf("日期结束跨到了其他日期：%v", to)
		}
	}
}

func TestInvalidOrMissingHostTimeZoneFallsBack(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetTimeZone("Invalid/TimeZone"); err == nil {
		t.Fatal("无效时区应返回错误")
	}
	if s.Location() != time.Local {
		t.Fatal("初次加载无效时区应保留本地回退")
	}
	if err := s.SetTimeZone("UTC"); err != nil {
		t.Fatal(err)
	}
	if s.Location() != time.UTC {
		t.Fatal("UTC 未生效")
	}
	if err := s.SetTimeZone(""); err != nil {
		t.Fatal(err)
	}
	if s.Location() != time.Local {
		t.Fatal("空时区应回退本地")
	}
}
