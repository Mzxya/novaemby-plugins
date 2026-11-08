package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func dashboardRecord(t *testing.T, s *Store, session, user, item, kind string, at time.Time, seconds int) string {
	t.Helper()
	rec := makeRecord(session, user, user, item, "内容 "+item, kind, at)
	end := at.Add(time.Duration(seconds) * time.Second)
	rec.EndedAt = &end
	rec.Completed = true
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestDashboardWindowAndUserScope(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	today := time.Date(2026, 11, 8, 0, 0, 0, 0, time.Local)
	dashboardRecord(t, s, "first", "Alice", "1", "Movie", today.AddDate(0, 0, -6), 60)
	dashboardRecord(t, s, "today", "Alice", "2", "Episode", today.Add(time.Hour), 120)
	dashboardRecord(t, s, "other-user", "Bob", "1", "Movie", today.Add(2*time.Hour), 180)
	dashboardRecord(t, s, "too-old", "Alice", "3", "Movie", today.AddDate(0, 0, -7), 500)
	dashboardRecord(t, s, "future", "Alice", "4", "Movie", now.Add(time.Hour), 500)
	got := s.Dashboard("", 7)
	if got.Stats.TotalPlayCount != 3 || got.Stats.TotalWatchSeconds != 360 || got.Stats.UniqueItems != 2 || got.Stats.CompletedCount != 3 {
		t.Fatalf("窗口统计不正确：%+v", got.Stats)
	}
	if got.Today.TotalPlayCount != 2 || got.Today.TotalWatchSeconds != 300 {
		t.Fatalf("今日统计不正确：%+v", got.Today)
	}
	if len(got.Daily) != 7 || got.From != "2026-11-02" || got.To != "2026-11-08" || len(got.Hours) != 24 {
		t.Fatalf("日期窗口不正确：%+v", got)
	}
	var dailySeconds, hourSeconds int64
	var dailyPlays, hourPlays int
	for _, d := range got.Daily {
		dailySeconds += d.WatchSeconds
		dailyPlays += d.PlayCount
	}
	for _, h := range got.Hours {
		hourSeconds += h.WatchSeconds
		hourPlays += h.PlayCount
	}
	if dailySeconds != got.Stats.TotalWatchSeconds || hourSeconds != dailySeconds || dailyPlays != 3 || hourPlays != 3 {
		t.Fatal("每日、时段和总计口径不一致")
	}
	alice := s.Dashboard("name:alice", 7)
	if alice.Stats.TotalPlayCount != 2 || alice.Today.TotalPlayCount != 1 || len(alice.Users) != 1 || alice.Users[0].Name != "Alice" {
		t.Fatalf("用户范围错误：%+v", alice)
	}
	if alice.Popular[0].TotalWatchSeconds != 120 {
		t.Fatal("热门内容同次数应按时长排序")
	}
	if got.Users[0].Key != "name:alice" {
		t.Fatal("并列时长排名应稳定按键排序")
	}
}
func TestDashboardUnfinishedSessionsAreIndependentOfWindow(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	for i := 0; i < 7; i++ {
		rec := makeRecord(fmt.Sprint(i), "u", "Alice", fmt.Sprint(i), "未结束", "Movie", now.AddDate(0, 0, -100-i))
		if _, _, err := s.Start(rec); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Dashboard("", 7)
	if got.ActiveTotal != 7 || len(got.Active) != 4 || got.Stats.TotalPlayCount != 0 || len(got.Recent) != 0 {
		t.Fatalf("未结束会话范围错误：active=%d shown=%d stats=%+v", got.ActiveTotal, len(got.Active), got.Stats)
	}
	for i := 1; i < len(got.Active); i++ {
		if got.Active[i].StartedAt.After(got.Active[i-1].StartedAt) {
			t.Fatal("未按最近开始排序")
		}
	}
	got.Active[0].ItemName = "篡改返回值"
	if s.Dashboard("", 7).Active[0].ItemName == "篡改返回值" {
		t.Fatal("返回值修改影响存储")
	}
	if s.Dashboard("name:someone", 7).ActiveTotal != 0 {
		t.Fatal("未结束会话没有按用户过滤")
	}
}
func TestDashboardRankingLimitsAndTypes(t *testing.T) {
	s := openTestStore(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now }
	for i := 0; i < 14; i++ {
		kind := "Movie"
		if i%3 == 1 {
			kind = "episode"
		}
		if i%3 == 2 {
			kind = "MusicVideo"
		}
		dashboardRecord(t, s, fmt.Sprint(i), fmt.Sprint(i), fmt.Sprint(i), kind, now.Add(-time.Duration(i+1)*time.Minute), i+1)
	}
	got := s.Dashboard("", 30)
	if len(got.Users) != 8 || len(got.Popular) != 5 || len(got.Recent) != 6 {
		t.Fatal("排行榜没有限制展示数量")
	}
	if len(got.Types) != 3 || got.Types[0].Count != 5 || got.Types[1].Count != 5 || got.Types[2].Count != 4 {
		t.Fatalf("类型归一不正确：%+v", got.Types)
	}
	if got.Stats.TotalPlayCount != 14 || got.Stats.UniqueItems != 14 {
		t.Fatal("展示上限不应截断统计")
	}
}
func TestDashboardEmptyAndHTTPValidation(t *testing.T) {
	s := openTestStore(t)
	a := &application{store: s}
	got := s.Dashboard("", 0)
	if got.Days != 1 {
		t.Fatal("下限未处理")
	}
	if s.Dashboard("", 999).Days != 90 {
		t.Fatal("上限未处理")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"active", "recent", "users", "popular", "daily", "hours", "types"} {
		if _, ok := body[key].([]any); !ok {
			t.Errorf("空 %s 应返回数组", key)
		}
	}
	handler := a.routes()["GET /history/dashboard"]
	if handler == nil {
		t.Fatal("未注册看板接口")
	}
	for _, days := range []string{"7", "30", "90", ""} {
		if r := handler(nil, &sdk.Request{Query: url.Values{"days": {days}}}); r.Status != http.StatusOK {
			t.Errorf("有效 days=%q 被拒绝", days)
		}
	}
	for _, days := range []string{"0", "-1", "8", "91", "abc", "7.5"} {
		if r := handler(nil, &sdk.Request{Query: url.Values{"days": {days}}}); r.Status != http.StatusBadRequest {
			t.Errorf("无效 days=%q 被接受", days)
		}
	}
}
