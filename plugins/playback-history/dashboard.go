package main

import (
	"sort"
	"strings"
	"time"
)

// DashboardResult 是在同一把读锁、同一个时刻生成的看板快照。
type DashboardResult struct {
	At          time.Time        `json:"at"`
	TimeZone    string           `json:"timeZone"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Days        int              `json:"days"`
	Stats       Stats            `json:"stats"`
	Today       Stats            `json:"today"`
	ActiveTotal int              `json:"activeTotal"`
	Active      []PlaybackRecord `json:"active"`
	Recent      []PlaybackRecord `json:"recent"`
	Users       []UserSummary    `json:"users"`
	Popular     []ItemSummary    `json:"popular"`
	Daily       []DailyBucket    `json:"daily"`
	Hours       []HourBucket     `json:"hours"`
	Types       []TypeBucket     `json:"types"`
}

type TypeBucket struct {
	Type    string `json:"type"`
	Count   int    `json:"count"`
	Seconds int64  `json:"seconds"`
}

// pushLatest 只保留少量可展示记录，避免克隆或排序整个历史库。
func pushLatest(rows []*PlaybackRecord, rec *PlaybackRecord, limit int) []*PlaybackRecord {
	at := sort.Search(len(rows), func(i int) bool {
		if rows[i].StartedAt.Equal(rec.StartedAt) {
			return rows[i].ID < rec.ID
		}
		return rows[i].StartedAt.Before(rec.StartedAt)
	})
	if at >= limit {
		return rows
	}
	rows = append(rows, nil)
	copy(rows[at+1:], rows[at:])
	rows[at] = rec
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (s *Store) Dashboard(user string, days int) DashboardResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	location := s.locationLocked()
	now := s.now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -(days - 1))
	result := DashboardResult{At: now, From: start.Format("2006-01-02"), To: today.Format("2006-01-02"), Days: days,
		TimeZone: location.String(),
		Active: []PlaybackRecord{}, Recent: []PlaybackRecord{}, Users: []UserSummary{}, Popular: []ItemSummary{},
		Daily: make([]DailyBucket, days), Hours: make([]HourBucket, 24), Types: []TypeBucket{}}
	dateIndex := map[string]int{}
	for i := range result.Daily {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		result.Daily[i].Date = date
		dateIndex[date] = i
	}
	for i := range result.Hours {
		result.Hours[i].Hour = i
	}
	users := map[string]*UserSummary{}
	userItems := map[string]map[string]bool{}
	items := map[string]*ItemSummary{}
	itemLatest := map[string]*PlaybackRecord{}
	types := map[string]*TypeBucket{}
	unique, todayUnique := map[string]bool{}, map[string]bool{}
	var active, recent []*PlaybackRecord
	for _, rec := range s.records {
		if !userMatches(rec, user) || rec.StartedAt.After(now) {
			continue
		}
		if rec.EndedAt == nil {
			result.ActiveTotal++
			active = pushLatest(active, rec, 4)
		}
		day, ok := dateIndex[rec.StartedAt.In(location).Format("2006-01-02")]
		if !ok {
			continue
		}
		secs := durationSecondsAt(rec, now)
		key := rec.ItemID
		if key == "" {
			key = "title:" + rec.DisplayTitle()
		}
		unique[key] = true
		result.Stats.TotalPlayCount++
		result.Stats.TotalWatchSeconds += secs
		if rec.Completed {
			result.Stats.CompletedCount++
		}
		if rec.EndedAt == nil {
			result.Stats.ActiveCount++
		}
		if day == days-1 {
			result.Today.TotalPlayCount++
			result.Today.TotalWatchSeconds += secs
			todayUnique[key] = true
			if rec.Completed {
				result.Today.CompletedCount++
			}
			if rec.EndedAt == nil {
				result.Today.ActiveCount++
			}
		}
		result.Daily[day].PlayCount++
		result.Daily[day].WatchSeconds += secs
		hour := rec.StartedAt.In(location).Hour()
		result.Hours[hour].PlayCount++
		result.Hours[hour].WatchSeconds += secs
		kind := rec.ItemType
		switch strings.ToLower(kind) {
		case "movie":
			kind = "Movie"
		case "episode":
			kind = "Episode"
		default:
			kind = "Other"
		}
		if types[kind] == nil {
			types[kind] = &TypeBucket{Type: kind}
		}
		types[kind].Count++
		types[kind].Seconds += secs
		uk := rec.UserKey()
		if users[uk] == nil {
			users[uk] = &UserSummary{Key: uk, ID: rec.UserID, Name: rec.UserName}
			userItems[uk] = map[string]bool{}
		}
		u := users[uk]
		u.PlayCount++
		u.TotalWatchSeconds += secs
		userItems[uk][key] = true
		if u.LastPlayedAt == nil || rec.StartedAt.After(*u.LastPlayedAt) {
			at := rec.StartedAt
			u.LastPlayedAt = &at
		}
		if items[key] == nil {
			items[key] = &ItemSummary{Key: key}
		}
		item := items[key]
		item.PlayCount++
		item.TotalWatchSeconds += secs
		if rec.Completed {
			item.CompletedCount++
		}
		if latest := itemLatest[key]; latest == nil || rec.StartedAt.After(latest.StartedAt) || (rec.StartedAt.Equal(latest.StartedAt) && rec.ID > latest.ID) {
			itemLatest[key] = rec
		}
		recent = pushLatest(recent, rec, 6)
	}
	result.Stats.UniqueItems = len(unique)
	result.Today.UniqueItems = len(todayUnique)
	for key, u := range users {
		u.UniqueItems = len(userItems[key])
		result.Users = append(result.Users, *u)
	}
	sort.Slice(result.Users, func(i, j int) bool {
		a, b := result.Users[i], result.Users[j]
		if a.TotalWatchSeconds != b.TotalWatchSeconds {
			return a.TotalWatchSeconds > b.TotalWatchSeconds
		}
		if a.PlayCount != b.PlayCount {
			return a.PlayCount > b.PlayCount
		}
		return a.Key < b.Key
	})
	if len(result.Users) > 8 {
		result.Users = result.Users[:8]
	}
	for key, item := range items {
		rec := itemLatest[key]
		item.ItemID = rec.ItemID
		item.SeriesID = rec.SeriesID
		item.Title = rec.DisplayTitle()
		item.ItemType = rec.ItemType
		item.Year = rec.Year
		item.LastPlayedAt = rec.StartedAt
		result.Popular = append(result.Popular, *item)
	}
	sort.Slice(result.Popular, func(i, j int) bool {
		a, b := result.Popular[i], result.Popular[j]
		if a.PlayCount != b.PlayCount {
			return a.PlayCount > b.PlayCount
		}
		if a.TotalWatchSeconds != b.TotalWatchSeconds {
			return a.TotalWatchSeconds > b.TotalWatchSeconds
		}
		return a.Key < b.Key
	})
	if len(result.Popular) > 5 {
		result.Popular = result.Popular[:5]
	}
	for _, kind := range []string{"Movie", "Episode", "Other"} {
		if t := types[kind]; t != nil {
			result.Types = append(result.Types, *t)
		}
	}
	for _, rec := range active {
		cp := cloneRecord(rec)
		cp.updateDuration(now)
		result.Active = append(result.Active, *cp)
	}
	for _, rec := range recent {
		cp := cloneRecord(rec)
		cp.updateDuration(now)
		result.Recent = append(result.Recent, *cp)
	}
	return result
}
