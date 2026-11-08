package main

import (
	"testing"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

// 兼容层对部分剧集不上报总时长（runtimeTicks=0），补全后也不能把事件里
// 已有的片名、类型、季集等有效信息抹掉。
func TestEnrichDoesNotEraseEventData(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 10, 50, 26, 0, time.UTC)
	rec := makeRecord("sess-1", "u1", "root", "135511", "啊呜 极恶的中学生是好朋友", "Episode", at)
	rec.PositionTicks = 768370008
	rec.RuntimeTicks = 0
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	season, episode := 1, 7
	// 宿主只回了最小信息：其它字段都为空/零。
	if err := s.Enrich(id, "135511", &sdk.Item{
		ID: "135511", Name: "", Type: "", RuntimeTicks: 0,
		SeriesID: "9001", SeriesName: "某剧", SeasonNumber: &season, EpisodeNumber: &episode,
	}, 90); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.ItemName != "啊呜 极恶的中学生是好朋友" || got.ItemType != "Episode" {
		t.Fatalf("补全把事件里的片名/类型抹掉了：%+v", got)
	}
	if got.SeriesID != "9001" || got.SeriesName != "某剧" {
		t.Fatalf("剧集级信息未保存：%+v", got)
	}
	if !got.Enriched {
		t.Fatal("补全后应标记 Enriched，避免重复请求宿主")
	}
}

// 总时长在 started 时缺失，到 stopped 才带上：合并后必须能算出进度并判定看完。
func TestStopMergesRuntimeTicksFromLaterEvent(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at.Add(2 * time.Hour) }
	start := makeRecord("sess-2", "u1", "root", "42", "测试电影", "Movie", at)
	start.RuntimeTicks = 0
	id, _, err := s.Start(start)
	if err != nil {
		t.Fatal(err)
	}
	stopped := stopRecord(start, at.Add(90*time.Minute), 6840*ticksPerSecond, 7200*ticksPerSecond)
	stopped.ItemName = "测试电影"
	if _, err := s.Stop(stopped, 90); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.RuntimeTicks != 7200*ticksPerSecond {
		t.Fatalf("停止事件里的总时长未合并：%d", got.RuntimeTicks)
	}
	if got.ProgressPercent() != 95 {
		t.Fatalf("进度 = %.2f%%，应为 95%%", got.ProgressPercent())
	}
	if !got.Completed {
		t.Fatal("达到 95% 应标记为已看完")
	}
}

// 补全发生在停止之后：补上总时长时也要按配置阈值（90%）判定，而不是只有 100% 才算。
func TestEnrichRecomputesCompletionWithThreshold(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at.Add(2 * time.Hour) }
	start := makeRecord("sess-3", "u1", "root", "77", "无时长剧集", "Episode", at)
	start.RuntimeTicks = 0
	id, _, err := s.Start(start)
	if err != nil {
		t.Fatal(err)
	}
	// 事件全程没有总时长，Stop 时无法判定完成度。
	stopped := stopRecord(start, at.Add(20*time.Minute), 6840*ticksPerSecond, 0)
	stopped.ItemName = "无时长剧集"
	if _, err := s.Stop(stopped, 90); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(id); got.Completed {
		t.Fatal("没有总时长时不应提前标记看完")
	}
	// 稍后宿主补全条目的总时长（7200 秒），此时位置 6840 秒 = 95%。
	if err := s.Enrich(id, "77", &sdk.Item{ID: "77", RuntimeTicks: 7200 * ticksPerSecond}, 90); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.RuntimeTicks != 7200*ticksPerSecond || !got.Completed {
		t.Fatalf("补全后应按 90%% 阈值判定完成：%+v", got)
	}
}

// 宿主不带总时长时，不能把事件里已有的有效时长覆盖成 0。
func TestEnrichKeepsExistingRuntimeTicks(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	rec := makeRecord("sess-4", "u1", "root", "54240", "测试电影", "Movie", at)
	rec.RuntimeTicks = 71400 * ticksPerSecond
	id, _, err := s.Start(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enrich(id, "54240", &sdk.Item{ID: "54240", Name: "测试电影", RuntimeTicks: 0}, 90); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(id); got.RuntimeTicks != 71400*ticksPerSecond {
		t.Fatalf("事件里的总时长被补全流程抹掉了：%d", got.RuntimeTicks)
	}
}

func TestApplyCompletionUsesOneRule(t *testing.T) {
	cases := []struct {
		name     string
		runtime  int64
		position int64
		percent  int
		signal   bool
		want     bool
	}{
		{"无总时长不判定", 0, 100, 90, false, false},
		{"事件信号为准", 0, 0, 90, true, true},
		{"低于阈值", 100, 89, 90, false, false},
		{"达到阈值", 100, 90, 90, false, true},
		{"超过总时长", 100, 120, 90, false, true},
		{"阈值为 0 时只认播完", 100, 95, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := PlaybackRecord{RuntimeTicks: c.runtime, PositionTicks: c.position}
			r.applyCompletion(c.percent, c.signal)
			if r.Completed != c.want {
				t.Fatalf("Completed = %v，应为 %v", r.Completed, c.want)
			}
		})
	}
}
