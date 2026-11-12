package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func captureRaw(event sdk.Event) []map[string]any {
	rows := []map[string]any{}
	logRawEvent(event, 1, func(message string, fields ...any) {
		row := map[string]any{"message": message}
		for i := 0; i+1 < len(fields); i += 2 {
			row[fields[i].(string)] = fields[i+1]
		}
		rows = append(rows, row)
	})
	return rows
}

func TestRawDiagnosticDefaultsOffAndManifestAgrees(t *testing.T) {
	if cfgBool(sdk.Config{}, "verboseEventLog", false) || cfgBool(sdk.Config{"verboseEventLog": false}, "verboseEventLog", false) {
		t.Fatal("诊断默认必须关闭")
	}
	if !cfgBool(sdk.Config{"verboseEventLog": true}, "verboseEventLog", false) {
		t.Fatal("无法开启诊断")
	}
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Config []struct {
			Key     string
			Type    string
			Default any
		}
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, config := range manifest.Config {
		if config.Key == "verboseEventLog" {
			if config.Type != "bool" || config.Default != false {
				t.Fatal("manifest 诊断开关必须为默认关闭的 bool")
			}
			return
		}
	}
	t.Fatal("配置页面缺少诊断开关")
}

func TestRawDiagnosticPreservesUnknownPayloadAndChineseSegments(t *testing.T) {
	raw := `{"未知结构":"` + strings.Repeat("中文🙂", 1000) + `","value":123}`
	event := sdk.Event{Name: "playback.started", At: time.Date(2026, 11, 11, 15, 7, 23, 0, time.UTC), Data: json.RawMessage(raw)}
	if _, err := decodePlayback(event.Data, event.At); err == nil {
		t.Fatal("样例应为未识别事件")
	}
	rows := captureRaw(event)
	if len(rows) < 3 || rows[0]["truncated"] != false || rows[0]["bytes"] != len(raw) {
		t.Fatal("诊断摘要不完整", rows[0])
	}
	var joined strings.Builder
	for _, row := range rows[1:] {
		if row["diagnosticId"] != rows[0]["diagnosticId"] || row["event"] != event.Name {
			t.Fatal("分段无法关联原事件")
		}
		chunk := row["raw"].(string)
		if !utf8.ValidString(chunk) || len([]rune(chunk)) > rawLogChunkRunes {
			t.Fatal("分段破坏字符或超过限制")
		}
		joined.WriteString(chunk)
	}
	if joined.String() != raw {
		t.Fatal("分段合并后不是原始数据")
	}
	if string(event.Data) != raw {
		t.Fatal("诊断不应修改事件数据")
	}
}

func TestRawDiagnosticEmptyAndOversizedPayload(t *testing.T) {
	empty := captureRaw(sdk.Event{Name: "user.updated"})
	if len(empty) != 2 || empty[1]["raw"] != "" || empty[1]["part"] != "1/1" {
		t.Fatal("空数据无法反馈")
	}
	raw := strings.Repeat("字", rawLogMaxRunes+1)
	rows := captureRaw(sdk.Event{Name: "playback.progress", Data: json.RawMessage(raw)})
	if rows[0]["truncated"] != true || rows[0]["bytes"] != len(raw) {
		t.Fatal("超长事件必须明确标记截断")
	}
	n := 0
	for _, row := range rows[1:] {
		n += len([]rune(row["raw"].(string)))
	}
	if n != rawLogMaxRunes {
		t.Fatal("超长事件没有受控截断")
	}
}
