package main

import (
	"strings"
	"testing"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func TestChunkRunesRoundTrip(t *testing.T) {
	samples := []string{
		"",
		"abc",
		strings.Repeat(`{"UQVNVII2":{"FQBAcs2Ewxg":"root"},"片名":"流浪地球2"}`, 120),
		strings.Repeat("中文混合 emoji 🙂 和 \"引号\" \n 换行", 90),
	}
	for _, s := range samples {
		parts := chunkRunes(s, logChunkRunes)
		if got := strings.Join(parts, ""); got != s {
			t.Fatalf("分片拼接后与原文不一致：原长 %d，还原 %d", len([]rune(s)), len([]rune(got)))
		}
		for i, part := range parts {
			if part == "" && s != "" {
				t.Fatalf("第 %d 片为空", i+1)
			}
			if len([]rune(part)) > logChunkRunes {
				t.Fatalf("第 %d 片超过分片上限", i+1)
			}
		}
	}
}

func TestFlattenEventFieldsObfuscated(t *testing.T) {
	raw := `{
		"UQVNVII2": {
			"EUebnB": "8b0d1149f7b4b79611d09ced064fb55f",
			"FQBAcs2Ewxg": "root",
			"Lp_fY7Tvs7": "BFD8AB74-4659-4C0B-AFB5-202500DED30F",
			"VapVfGw8id": "iPhone",
			"P0aX1v": 2591,
			"V3S9zRa": true
		},
		"Kx9": "流浪地球2"
	}`
	fields := flattenEventFields([]byte(raw))
	if len(fields) != 7 {
		t.Fatalf("字段数不正确：%d，明细 %+v", len(fields), fields)
	}
	lines := strings.Join(fieldLines(fields), "\n")
	for _, want := range []string{
		"UQVNVII2.FQBAcs2Ewxg = root (str)",
		"UQVNVII2.VapVfGw8id = iPhone (str)",
		"UQVNVII2.P0aX1v = 2591 (num)",
		"UQVNVII2.V3S9zRa = true (bool)",
		"Kx9 = 流浪地球2 (str)",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("字段清单缺少 %q\n%s", want, lines)
		}
	}
	for i := 1; i < len(fields); i++ {
		if fields[i-1].Path > fields[i].Path {
			t.Fatal("字段清单未按路径排序")
		}
	}
}

func TestFlattenEventFieldsInvalidData(t *testing.T) {
	for _, raw := range []string{"", "{", "null", "[]", `"text"`} {
		fields := flattenEventFields([]byte(raw))
		if len(fieldLines(fields)) == 0 {
			t.Fatalf("非法数据 %q 应返回占位说明", raw)
		}
	}
}

func TestVerboseLogDefaultOff(t *testing.T) {
	// 字段映射已确认，诊断日志默认关闭，避免刷屏挤掉正常日志；需要时仍可显式打开。
	if configBool(sdk.Config{}, "verboseEventLog", defaultVerboseEventLog) {
		t.Fatal("空配置下完整日志应默认关闭")
	}
	if !configBool(sdk.Config{"verboseEventLog": true}, "verboseEventLog", defaultVerboseEventLog) {
		t.Fatal("显式开启时应生效")
	}
}

func TestPlaybackLogAction(t *testing.T) {
	cases := map[string]string{
		"playback.started": "开始播放",
		"playback.stopped": "停止播放",
		"playback.paused":  "",
		"user.created":     "",
	}
	for ev, want := range cases {
		if got := playbackLogAction(ev); got != want {
			t.Fatalf("%s => %q，应为 %q", ev, got, want)
		}
	}
}
