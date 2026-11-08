package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

// 可选的完整诊断日志：把宿主送来的事件完整地写进插件日志，便于排查字段结构。
// 默认关闭，启用后才解析诊断字段并输出日志。
// 宿主日志可能对单条消息有长度限制，因此按 rune 分片输出，拼接后即为完整原文。
const (
	logChunkRunes    = 600
	maxLogEventRunes = 64 * 1024
)

type eventField struct {
	Path  string
	Type  string
	Value string
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return s
}

// flattenEventFields 把事件 JSON 展开成「路径 = 值（类型）」清单。
// 字段名被混淆时，这份清单能直接看出每个随机键对应什么值。
func flattenEventFields(data []byte) []eventField {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	var flat []flatValue
	flatten(root, nil, 0, &flat) // 复用 eventshape.go 的递归展开
	out := make([]eventField, 0, len(flat))
	for _, f := range flat {
		typ, val := "null", ""
		switch v := f.value.(type) {
		case string:
			typ, val = "str", v
		case float64:
			typ, val = "num", strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			typ, val = "bool", strconv.FormatBool(v)
		default:
			typ, val = "other", fmt.Sprint(v)
		}
		runes := []rune(val)
		if len(runes) > 200 {
			val = string(runes[:200]) + "…(此值截断，完整内容见事件原始数据)"
		}
		out = append(out, eventField{
			Path:  strings.Join(f.path, "."),
			Type:  typ,
			Value: oneLine(val),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func fieldLines(fields []eventField) []string {
	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		lines = append(lines, fmt.Sprintf("%s = %s (%s)", f.Path, f.Value, f.Type))
	}
	if len(lines) == 0 {
		lines = append(lines, "（事件体不是可展开的 JSON 对象，请直接看事件原始数据）")
	}
	return lines
}

// chunkRunes 按字符切分，避免把中文切成半个字；拼接后与原文完全一致。
func chunkRunes(s string, size int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	if size < 1 {
		size = logChunkRunes
	}
	out := make([]string, 0, (len(runes)+size-1)/size)
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

// logEventFull 在插件日志里输出：事件摘要 → 字段清单 → 完整原始 JSON。
// 每条分片都带 part=i/n，复制日志时按顺序拼接即可还原。
func (a *application) logEventFull(h *sdk.Host, ev sdk.Event) {
	if !configBool(h.Config(), "verboseEventLog", defaultVerboseEventLog) {
		return
	}
	fields := flattenEventFields(ev.Data)
	raw := string(ev.Data)
	if len([]rune(raw)) > maxLogEventRunes {
		raw = string([]rune(raw)[:maxLogEventRunes]) + "…(超出上限已截断)"
	}
	h.Info("收到事件·完整诊断已开启", "event", ev.Name,
		"at", eventTime(ev).Format(time.RFC3339Nano),
		"bytes", len(ev.Data), "fields", len(fields))
	if ev.Name == "playback.paused" {
		// 暂停/进度事件很频繁（拖动一次就来好几条），日志只留摘要避免刷屏；
		h.Info("暂停事件仅输出摘要，日志省略分片", "event", ev.Name, "bytes", len(ev.Data))
		return
	}

	fieldParts := chunkRunes(strings.Join(fieldLines(fields), " | "), logChunkRunes)
	for i, part := range fieldParts {
		h.Info("事件字段清单", "event", ev.Name,
			"part", fmt.Sprintf("%d/%d", i+1, len(fieldParts)), "list", part)
	}
	rawParts := chunkRunes(raw, logChunkRunes)
	for i, part := range rawParts {
		h.Info("事件原始数据", "event", ev.Name,
			"part", fmt.Sprintf("%d/%d", i+1, len(rawParts)), "data", part)
	}
}

func playbackLogAction(eventName string) string {
	switch eventName {
	case "playback.started":
		return "开始播放"
	case "playback.stopped":
		return "停止播放"
	default:
		return ""
	}
}
