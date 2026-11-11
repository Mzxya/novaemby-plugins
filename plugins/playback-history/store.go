package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

type diskOperation struct {
	Op     string          `json:"op"`
	ID     string          `json:"id,omitempty"`
	Record *PlaybackRecord `json:"record,omitempty"`
	User   *KnownUser      `json:"user,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	file    *os.File
	records map[string]*PlaybackRecord
	open    map[string]string
	users   map[string]KnownUser
	now      func() time.Time
	location *time.Location
	// 进度仅更新现有会话；记录第一次未持久化更新的时间，最多每 30 秒追加一次。
	progressPending map[string]time.Time
	// 写盘节流状态（均受 mu 保护）：lastSync 为上次 fsync 时间，dirty 表示有未落盘增量。
	lastSync time.Time
	dirty    bool
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, file: f, records: map[string]*PlaybackRecord{}, open: map[string]string{}, users: map[string]KnownUser{}, now: time.Now}
	if err := s.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	lineNo := 0
	var offset, lastGoodOffset int64
	var lastBad error
	for scanner.Scan() {
		lineNo++
		offset += int64(len(scanner.Bytes()) + 1)
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if lastBad != nil {
			return fmt.Errorf("数据文件第 %d 行损坏：%w", lineNo-1, lastBad)
		}
		var op diskOperation
		if err := json.Unmarshal([]byte(line), &op); err != nil {
			lastBad = err // 允许忽略进程崩溃时最后一条未写完的记录。
			continue
		}
		switch op.Op {
		case "put":
			if op.Record != nil && op.Record.ID != "" {
				rec := *op.Record
				s.records[rec.ID] = &rec
			}
		case "delete":
			delete(s.records, op.ID)
		case "user":
			if op.User != nil && op.User.Key != "" {
				s.users[op.User.Key] = *op.User
			}
		}
		lastGoodOffset = offset
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if lastBad != nil {
		// 丢弃最后一条不完整的 JSON，避免后续追加内容与它粘连。
		if err := s.file.Truncate(lastGoodOffset); err != nil {
			return err
		}
		if _, err := s.file.Seek(lastGoodOffset, io.SeekStart); err != nil {
			return err
		}
	}
	// 旧版数据按用户 ID 归组，迁移到按用户名归组，避免同名用户在列表里出现两次。
	s.migrateUsersLocked()
	s.rebuildOpenLocked()
	return nil
}

// migrateUsersLocked 把旧键（id:xxx）的用户条目合并到按用户名的新键（name:yyy）下。
// 只调整内存，后续写入自然使用新键；旧键行留在数据文件里，重启加载时同样被合并。
func (s *Store) migrateUsersLocked() {
	for key, user := range s.users {
		if !strings.HasPrefix(key, "id:") || strings.TrimSpace(user.Name) == "" {
			continue
		}
		newKey := userKey("", user.Name)
		if newKey == key {
			continue
		}
		delete(s.users, key)
		if existing, ok := s.users[newKey]; ok {
			if existing.Name == "" {
				existing.Name = user.Name
			}
			if existing.ID == "" {
				existing.ID = user.ID
			}
			if user.LastSeen.After(existing.LastSeen) {
				existing.LastSeen = user.LastSeen
			}
			existing.Deleted = existing.Deleted || user.Deleted
			s.users[newKey] = existing
		} else {
			user.Key = newKey
			s.users[newKey] = user
		}
	}
}

func (s *Store) rebuildOpenLocked() {
	s.open = map[string]string{}
	for id, rec := range s.records {
		if rec.EndedAt == nil {
			s.open[openKey(rec)] = id
		}
	}
}

func openKey(r *PlaybackRecord) string {
	if r.SessionID != "" {
		return "session:" + r.SessionID
	}
	item := r.ItemID
	if item == "" {
		item = "title:" + r.ItemName // 没有条目 ID 时用片名区分，避免不同影片被合并成一条
	}
	return "item:" + r.UserKey() + "|" + item
}

func newRecordID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func cloneRecord(r *PlaybackRecord) *PlaybackRecord {
	cp := *r
	if r.EndedAt != nil {
		t := *r.EndedAt
		cp.EndedAt = &t
	}
	return &cp
}

// syncInterval 内的多次写盘只做一次 fsync：弱主机（NAS、低功耗盒子）上每次
// fsync 都要等存储确认，逐条同步会让事件处理明显变慢。崩溃时最多丢最近
// syncInterval 内的增量；记录完结（Stop）、停机（Close）与清理重写时强制落盘。
const syncInterval = 3 * time.Second

func (s *Store) appendLocked(op diskOperation) error {
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := s.file.Write(b); err != nil {
		return err
	}
	if op.Record != nil {
		delete(s.progressPending, op.Record.ID)
	} else if op.Op == "delete" {
		delete(s.progressPending, op.ID)
	}
	s.dirty = true
	now := s.now()
	if s.lastSync.IsZero() || now.Sub(s.lastSync) >= syncInterval {
		return s.forceSyncLocked()
	}
	return nil
}

// forceSyncLocked 立即把未落盘的增量写入存储并记录同步时间。
func (s *Store) forceSyncLocked() error {
	if !s.dirty {
		return nil
	}
	if err := s.file.Sync(); err != nil {
		return err
	}
	s.dirty = false
	s.lastSync = s.now()
	return nil
}

func mergeEventFields(dst *PlaybackRecord, src PlaybackRecord) {
	if src.UserID != "" {
		dst.UserID = src.UserID
	}
	if src.UserName != "" {
		dst.UserName = src.UserName
	}
	if src.ItemName != "" {
		dst.ItemName = src.ItemName
	}
	if src.ItemType != "" {
		dst.ItemType = src.ItemType
	}
	if src.DeviceName != "" {
		dst.DeviceName = src.DeviceName
	}
	if src.Client != "" {
		dst.Client = src.Client
	}
	if src.PlayMethod != "" {
		dst.PlayMethod = src.PlayMethod
	}
	if src.RemoteAddr != "" {
		dst.RemoteAddr = src.RemoteAddr
	}
	// 部分条目在 started 事件里没有总时长、到 stopped 才带上；
	// 只在收到有效值时更新，避免用 0 抹掉已补全的时长。
	if src.RuntimeTicks > 0 {
		dst.RuntimeTicks = src.RuntimeTicks
	}
	dst.PositionTicks = src.PositionTicks
	dst.LastEventAt = src.LastEventAt
}

func (s *Store) Start(rec PlaybackRecord) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openKey(&rec)
	if id, ok := s.open[key]; ok {
		if current := s.records[id]; current != nil {
			cp := cloneRecord(current)
			mergeEventFields(cp, rec)
			cp.updateDuration(s.now())
			if err := s.appendLocked(diskOperation{Op: "put", Record: cp}); err != nil {
				return id, false, err
			}
			s.records[id] = cp
			return id, false, nil
		}
	}
	id, err := newRecordID()
	if err != nil {
		return "", false, err
	}
	rec.ID = id
	rec.StartedAt = rec.StartedAt.UTC()
	rec.LastEventAt = rec.LastEventAt.UTC()
	rec.updateDuration(s.now())
	if err := s.appendLocked(diskOperation{Op: "put", Record: &rec}); err != nil {
		return "", false, err
	}
	s.records[id] = cloneRecord(&rec)
	s.open[key] = id
	return id, true, nil
}

func (s *Store) findOpenLocked(rec *PlaybackRecord) (string, *PlaybackRecord) {
	if id, ok := s.open[openKey(rec)]; ok && s.records[id] != nil {
		return id, s.records[id]
	}
	var foundID string
	var found *PlaybackRecord
	for _, id := range s.open {
		candidate := s.records[id]
		if candidate == nil || candidate.ItemID != rec.ItemID || candidate.UserKey() != rec.UserKey() {
			continue
		}
		if found == nil || candidate.StartedAt.After(found.StartedAt) {
			foundID, found = id, candidate
		}
	}
	return foundID, found
}

func (s *Store) Pause(rec PlaybackRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, current := s.findOpenLocked(&rec)
	if current == nil {
		return "", nil
	}
	cp := cloneRecord(current)
	mergeEventFields(cp, rec)
	cp.updateDuration(s.now())
	if err := s.appendLocked(diskOperation{Op: "put", Record: cp}); err != nil {
		return id, err
	}
	s.records[id] = cp
	return id, nil
}

const progressWriteInterval = 30 * time.Second

func (s *Store) FlushProgress() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return os.ErrClosed
	}
	now := s.now()
	for id, first := range s.progressPending {
		if now.Sub(first) < progressWriteInterval {
			continue
		}
		if rec := s.records[id]; rec != nil {
			if err := s.appendLocked(diskOperation{Op: "put", Record: rec}); err != nil {
				return err
			}
		} else {
			delete(s.progressPending, id)
		}
	}
	return nil
}

// Progress 不创建播放记录，也不按其他设备的用户/条目兜底合并。
// 内存每次更新，JSONL 每个会话最多每 30 秒追加一次；停止、重写和关闭会保存最新值。
func (s *Store) Progress(rec PlaybackRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return os.ErrClosed
	}
	if rec.SessionID == "" && (rec.ItemID == "" || (rec.UserID == "" && rec.UserName == "")) {
		return nil
	}
	id := s.open[openKey(&rec)]
	current := s.records[id]
	if current == nil || current.EndedAt != nil || !rec.LastEventAt.After(current.LastEventAt) {
		return nil
	}
	if rec.ItemID != "" && current.ItemID != "" && rec.ItemID != current.ItemID {
		return nil // 同一设备已经切换到另一条目时，忽略旧条目的迟到上报。
	}
	cp := cloneRecord(current)
	cp.PositionTicks = max(0, rec.PositionTicks)
	cp.LastEventAt = rec.LastEventAt.UTC()
	if rec.RuntimeTicks > 0 {
		cp.RuntimeTicks = rec.RuntimeTicks
	}
	cp.updateDuration(s.now())
	if s.progressPending == nil {
		s.progressPending = map[string]time.Time{}
	}
	now := s.now()
	first, pending := s.progressPending[id]
	if pending && now.Sub(first) >= progressWriteInterval {
		if err := s.appendLocked(diskOperation{Op: "put", Record: cp}); err != nil {
			return err
		}
	} else if !pending {
		s.progressPending[id] = now
	}
	s.records[id] = cp
	return nil
}

func (s *Store) Stop(rec PlaybackRecord, completionPercent int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, current := s.findOpenLocked(&rec)
	created := false
	if current == nil {
		var err error
		id, err = newRecordID()
		if err != nil {
			return "", err
		}
		created = true
		current = &rec
		current.ID = id
		if current.PositionTicks > 0 {
			d := time.Duration(current.PositionTicks/ticksPerSecond) * time.Second
			if d > 0 && d < 24*time.Hour {
				current.StartedAt = current.LastEventAt.Add(-d)
				current.StartPositionTicks = 0
			}
		}
	}
	cp := cloneRecord(current)
	mergeEventFields(cp, rec)
	ended := rec.LastEventAt.UTC()
	cp.EndedAt = &ended
	// 统一口径判定，且保留此前由补全流程标下的「已看完」。
	cp.applyCompletion(completionPercent, rec.Completed)
	cp.updateDuration(s.now())
	if err := s.appendLocked(diskOperation{Op: "put", Record: cp}); err != nil {
		return id, err
	}
	// 记录到此完结，立即落盘，避免崩溃丢失这条完整记录。
	if err := s.forceSyncLocked(); err != nil {
		return id, err
	}
	s.records[id] = cp
	if !created {
		delete(s.open, openKey(current))
	}
	delete(s.open, openKey(&rec))
	delete(s.open, openKey(cp))
	return id, nil
}

func (s *Store) Enrich(id, itemID string, item *sdk.Item, completionPercent int) error {
	if item == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.records[id]
	if current == nil {
		return nil
	}
	cp := cloneRecord(current)
	if itemID != "" {
		cp.ItemID = itemID
	}
	cp.ItemIDCandidates = nil
	cp.Enriched = true
	// 宿主返回的条目信息可能不完整：只覆盖非空字段，
	// 否则会用 0/空值抹掉事件里已经拿到的片名、季集和总时长。
	if item.Name != "" {
		cp.ItemName = item.Name
	}
	if item.Type != "" {
		cp.ItemType = item.Type
	}
	if item.SeriesName != "" {
		cp.SeriesName = item.SeriesName
	}
	if item.SeriesID != "" {
		cp.SeriesID = item.SeriesID
	}
	if item.SeasonNumber != nil {
		cp.SeasonNumber = item.SeasonNumber
	}
	if item.EpisodeNumber != nil {
		cp.EpisodeNumber = item.EpisodeNumber
	}
	if item.Year != 0 {
		cp.Year = item.Year
	}
	if item.RuntimeTicks > 0 {
		cp.RuntimeTicks = item.RuntimeTicks
	}
	cp.applyCompletion(completionPercent, false)
	if err := s.appendLocked(diskOperation{Op: "put", Record: cp}); err != nil {
		return err
	}
	s.records[id] = cp
	return nil
}

func (s *Store) AddUser(user KnownUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.users[user.Key]; ok {
		if user.Name == "" {
			user.Name = existing.Name
		}
		if user.ID == "" {
			user.ID = existing.ID
		}
		if existing.LastSeen.After(user.LastSeen) {
			user.LastSeen = existing.LastSeen
		}
	}
	if err := s.appendLocked(diskOperation{Op: "user", User: &user}); err != nil {
		return err
	}
	s.users[user.Key] = user
	return nil
}

func (s *Store) Get(id string) (PlaybackRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[id]
	if !ok {
		return PlaybackRecord{}, false
	}
	cp := cloneRecord(rec)
	cp.updateDuration(s.now())
	return *cp, true
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// userMatches 判断记录是否属于指定用户；key 可以是用户归组键、用户 ID 或用户名。
func userMatches(rec *PlaybackRecord, key string) bool {
	if key == "" {
		return true
	}
	return rec.UserKey() == key || rec.UserID == key || strings.EqualFold(rec.UserName, key)
}

func matches(rec *PlaybackRecord, q Query) bool {
	if !userMatches(rec, q.UserKey) {
		return false
	}
	if q.ItemType != "" && !strings.EqualFold(rec.ItemType, q.ItemType) {
		return false
	}
	if !q.From.IsZero() && rec.StartedAt.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && rec.StartedAt.After(q.To) {
		return false
	}
	switch q.Status {
	case "completed":
		if !rec.Completed {
			return false
		}
	case "incomplete":
		if rec.Completed || rec.EndedAt == nil {
			return false
		}
	case "active":
		if rec.EndedAt != nil {
			return false
		}
	}
	if q.Search != "" {
		haystack := strings.ToLower(strings.Join([]string{rec.DisplayTitle(), rec.ItemName, rec.SeriesName, rec.UserName, rec.DeviceName, rec.Client}, " "))
		if !strings.Contains(haystack, strings.ToLower(q.Search)) {
			return false
		}
	}
	return true
}

// sortBy 决定记录排序：默认最近播放在前；duration 按单次播放时长。
func (s *Store) sortBy(q Query) func(i, j *PlaybackRecord) bool {
	return func(a, b *PlaybackRecord) bool {
		if q.SortBy == "duration" {
			da, db := durationSecondsAt(a, s.now()), durationSecondsAt(b, s.now())
			if da != db {
				return da > db
			}
			return a.StartedAt.After(b.StartedAt)
		}
		if q.SortBy == "oldest" {
			if a.StartedAt.Equal(b.StartedAt) {
				return a.ID < b.ID
			}
			return a.StartedAt.Before(b.StartedAt)
		}
		if a.StartedAt.Equal(b.StartedAt) {
			return a.ID > b.ID
		}
		return a.StartedAt.After(b.StartedAt)
	}
}

// filteredLocked 返回匹配记录的内部指针（仅在持有锁期间使用），
// 不做整条 clone：排序只依赖时间/时长，clone 留给最终返回的那一小页。
func (s *Store) filteredLocked(q Query) []*PlaybackRecord {
	out := make([]*PlaybackRecord, 0, len(s.records))
	for _, rec := range s.records {
		if matches(rec, q) {
			out = append(out, rec)
		}
	}
	cmp := s.sortBy(q)
	sort.Slice(out, func(i, j int) bool { return cmp(out[i], out[j]) })
	return out
}

func calculateStats(records []*PlaybackRecord, now time.Time) Stats {
	stats := Stats{TotalPlayCount: len(records)}
	unique := map[string]struct{}{}
	for _, rec := range records {
		key := rec.ItemID
		if key == "" {
			key = rec.DisplayTitle()
		}
		unique[key] = struct{}{}
		stats.TotalWatchSeconds += durationSecondsAt(rec, now)
		if rec.Completed {
			stats.CompletedCount++
		}
		if rec.EndedAt == nil {
			stats.ActiveCount++
		}
	}
	stats.UniqueItems = len(unique)
	return stats
}

func (s *Store) Query(q Query) QueryResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.filteredLocked(q)
	now := s.now()
	result := QueryResult{Total: len(all), Page: q.Page, PageSize: q.PageSize, Stats: calculateStats(all, now), Records: []PlaybackRecord{}}
	if q.Page < 1 {
		result.Page = 1
	}
	if q.PageSize < 1 {
		result.PageSize = 30
	}
	if q.PageSize > 500 {
		result.PageSize = 500
	}
	result.Pages = (result.Total + result.PageSize - 1) / result.PageSize
	if result.Pages > 0 && result.Page > result.Pages {
		result.Page = result.Pages
	}
	start := (result.Page - 1) * result.PageSize
	if start >= len(all) {
		return result
	}
	end := start + result.PageSize
	if end > len(all) {
		end = len(all)
	}
	// 只对当前页（最多 500 条）做 clone 和时长刷新，
	// 大库时避免每次查询都克隆全部匹配记录。
	for _, rec := range all[start:end] {
		cp := cloneRecord(rec)
		cp.updateDuration(now)
		result.Records = append(result.Records, *cp)
	}
	return result
}

func (s *Store) Items(q Query) ItemResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	matched := make([]*PlaybackRecord, 0, len(s.records))
	for _, rec := range s.records {
		if matches(rec, q) {
			matched = append(matched, rec)
		}
	}
	type agg struct {
		sum      ItemSummary
		latest   *PlaybackRecord
		latestAt time.Time
	}
	byKey := map[string]*agg{}
	var order []string
	for _, rec := range matched {
		key := rec.ItemID
		if key == "" {
			key = "title:" + rec.DisplayTitle()
		}
		a := byKey[key]
		if a == nil {
			a = &agg{sum: ItemSummary{Key: key}}
			byKey[key] = a
			order = append(order, key)
		}
		a.sum.PlayCount++
		a.sum.TotalWatchSeconds += durationSecondsAt(rec, now)
		if rec.Completed {
			a.sum.CompletedCount++
		}
		if rec.StartedAt.After(a.latestAt) {
			a.latestAt = rec.StartedAt
			a.latest = rec
		}
	}
	items := make([]ItemSummary, 0, len(byKey))
	for _, key := range order {
		a := byKey[key]
		if a.latest != nil {
			a.sum.ItemID = a.latest.ItemID
			a.sum.SeriesID = a.latest.SeriesID
			a.sum.Title = a.latest.DisplayTitle()
			a.sum.ItemType = a.latest.ItemType
			a.sum.Year = a.latest.Year
		}
		a.sum.LastPlayedAt = a.latestAt
		items = append(items, a.sum)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		switch q.SortBy {
		case "count":
			if a.PlayCount != b.PlayCount {
				return a.PlayCount > b.PlayCount
			}
		case "recent":
			if !a.LastPlayedAt.Equal(b.LastPlayedAt) {
				return a.LastPlayedAt.After(b.LastPlayedAt)
			}
		default: // duration：总观看时长最长在前
			if a.TotalWatchSeconds != b.TotalWatchSeconds {
				return a.TotalWatchSeconds > b.TotalWatchSeconds
			}
		}
		return a.Title < b.Title
	})
	result := ItemResult{Total: len(items), Page: q.Page, PageSize: q.PageSize, Records: []ItemSummary{}, Stats: calculateStats(matched, now)}
	if result.Page < 1 {
		result.Page = 1
	}
	if result.PageSize < 1 {
		result.PageSize = 30
	}
	if result.PageSize > 500 {
		result.PageSize = 500
	}
	result.Pages = (result.Total + result.PageSize - 1) / result.PageSize
	if result.Pages > 0 && result.Page > result.Pages {
		result.Page = result.Pages
	}
	start := (result.Page - 1) * result.PageSize
	if start < len(items) {
		end := start + result.PageSize
		if end > len(items) {
			end = len(items)
		}
		result.Records = append(result.Records, items[start:end]...)
	}
	return result
}

func (s *Store) Users() []UserSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	byKey := map[string]*UserSummary{}
	unique := map[string]map[string]struct{}{}
	ensure := func(key, id, name string) *UserSummary {
		if byKey[key] == nil {
			byKey[key] = &UserSummary{Key: key, ID: id, Name: name}
			unique[key] = map[string]struct{}{}
		}
		return byKey[key]
	}
	for key, user := range s.users {
		u := ensure(key, user.ID, user.Name)
		u.Deleted = user.Deleted
	}
	for _, rec := range s.records {
		// 汇总统计只需要时长，用纯计算函数避免逐条 clone 整条记录。
		u := ensure(rec.UserKey(), rec.UserID, rec.UserName)
		if u.Name == "" {
			u.Name = rec.UserName
		}
		u.PlayCount++
		u.TotalWatchSeconds += durationSecondsAt(rec, now)
		itemKey := rec.ItemID
		if itemKey == "" {
			itemKey = rec.DisplayTitle()
		}
		unique[rec.UserKey()][itemKey] = struct{}{}
		if u.LastPlayedAt == nil || rec.StartedAt.After(*u.LastPlayedAt) {
			t := rec.StartedAt
			u.LastPlayedAt = &t
		}
	}
	out := make([]UserSummary, 0, len(byKey))
	for key, user := range byKey {
		if user.Deleted && user.PlayCount == 0 {
			continue
		}
		user.UniqueItems = len(unique[key])
		out = append(out, *user)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastPlayedAt == nil && out[j].LastPlayedAt != nil {
			return false
		}
		if out[i].LastPlayedAt != nil && out[j].LastPlayedAt == nil {
			return true
		}
		if out[i].LastPlayedAt != nil && !out[i].LastPlayedAt.Equal(*out[j].LastPlayedAt) {
			return out[i].LastPlayedAt.After(*out[j].LastPlayedAt)
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[id]
	if rec == nil {
		return false, nil
	}
	if err := s.appendLocked(diskOperation{Op: "delete", ID: id}); err != nil {
		return false, err
	}
	delete(s.records, id)
	delete(s.open, openKey(rec))
	return true, nil
}

func (s *Store) Clear(key string, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, rec := range s.records {
		if key != "" && rec.UserKey() != key && rec.UserID != key && !strings.EqualFold(rec.UserName, key) {
			continue
		}
		if !before.IsZero() && !rec.StartedAt.Before(before) {
			continue
		}
		delete(s.records, id)
		removed++
	}
	if removed == 0 {
		return 0, nil
	}
	s.rebuildOpenLocked()
	if err := s.rewriteLocked(); err != nil {
		return 0, err
	}
	return removed, nil
}

func (s *Store) Cleanup(retentionDays, maxRecords int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	remove := map[string]bool{}
	if retentionDays > 0 {
		cutoff := s.now().In(s.locationLocked()).AddDate(0, 0, -retentionDays)
		for id, rec := range s.records {
			if rec.StartedAt.Before(cutoff) {
				remove[id] = true
			}
		}
	}
	if maxRecords > 0 && len(s.records)-len(remove) > maxRecords {
		remaining := make([]*PlaybackRecord, 0, len(s.records)-len(remove))
		for id, rec := range s.records {
			if !remove[id] {
				remaining = append(remaining, rec)
			}
		}
		sort.Slice(remaining, func(i, j int) bool { return remaining[i].StartedAt.After(remaining[j].StartedAt) })
		for _, rec := range remaining[maxRecords:] {
			remove[rec.ID] = true
		}
	}
	if len(remove) == 0 {
		return 0, nil
	}
	for id := range remove {
		delete(s.records, id)
	}
	s.rebuildOpenLocked()
	if err := s.rewriteLocked(); err != nil {
		return 0, err
	}
	return len(remove), nil
}

func (s *Store) rewriteLocked() error {
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".history-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	writer := bufio.NewWriter(tmp)
	encoder := json.NewEncoder(writer)
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := encoder.Encode(diskOperation{Op: "put", Record: s.records[id]}); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(s.users))
	for key := range s.users {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		user := s.users[key]
		if err := encoder.Encode(diskOperation{Op: "user", User: &user}); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := s.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		s.file, _ = os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.file = f
	s.lastSync = time.Time{}
	s.dirty = false
	clear(s.progressPending)
	ok = true
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	var flushErr error
	for id := range s.progressPending {
		if rec := s.records[id]; rec != nil {
			if err := s.appendLocked(diskOperation{Op: "put", Record: rec}); err != nil {
				flushErr = err
				break
			}
		}
	}
	if err := s.forceSyncLocked(); err != nil && flushErr == nil {
		flushErr = err
	}
	err := s.file.Close()
	s.file = nil
	if err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return flushErr
}
