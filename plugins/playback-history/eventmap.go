package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// 依据用户实测的完整事件原文确认的字段映射。
// 该兼容层把字段名替换成固定随机串（同版本内不变），因此可以直接精确取值；
// 键名一旦变化，parsePlaybackEvent 会自动回退到按值形状解析。
const (
	obfSession       = "UQVNVII2"
	obfUserID        = "EUebnB"
	obfDeviceID      = "Ma1NLTTGgLkj"
	obfUserName      = "FQBAcs2Ewxg"
	obfDeviceGUID    = "Lp_fY7Tvs7"
	obfDeviceName    = "VapVfGw8id"
	obfClient        = "ZgySkQw"
	obfClientVersion = "Thafucf0N"
	obfItemID        = "P0aX1v"  // 数字形式的媒体库条目 ID
	obfItemIDStr     = "V3S9zRa" // 同一个 ID 的字符串形式
	obfPlaySession   = "Sk2P5N"  // 单次播放会话 ID：每次播放不同，同一会话内不变
	obfPositionTicks = "AEKpVZufJ"
	obfRuntimeTicks  = "Hq1fULgr1q"
	obfIsPaused      = "VUWeWNJXBmau"
	obfSessionStart  = "KwVN1_8Euh9d"
	obfLastActivity  = "GgzQPs"

	obfItemType  = "XaNoaNs"
	obfItemName  = "VE9xvSKkbW4R"
	obfEventTime = "IEbFReOhlBe"
)

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func int64Value(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
}

func numberString(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case string:
		return strings.TrimSpace(t)
	}
	return ""
}

// parseKnownShape 按已确认的键名精确解析，避免把条目 ID 当成播放位置、
// 把用户 ID 当成条目 ID（旧版按值形状猜测时会出现这两个错误）。
func parseKnownShape(data []byte) (parsedEvent, bool) {
	p := parsedEvent{Source: "mapped"}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return p, false
	}
	sess := asMap(root[obfSession])
	if sess == nil {
		return p, false
	}
	userName := stringValue(sess[obfUserName])
	itemName := stringValue(root[obfItemName])
	if userName == "" || itemName == "" {
		return p, false // 字段名已变化，交给通用解析
	}

	p.UserName = userName
	p.UserID = stringValue(sess[obfUserID])
	p.SessionID = stringValue(sess[obfPlaySession])
	if p.SessionID == "" {
		p.SessionID = stringValue(sess[obfDeviceGUID])
	}
	p.ItemName = itemName
	if typ := normalizeItemType(stringValue(root[obfItemType])); typ != "" {
		p.ItemType = typ
	} else {
		p.ItemType = stringValue(root[obfItemType])
	}
	p.DeviceName = stringValue(sess[obfDeviceName])
	p.Client = stringValue(sess[obfClient])

	// 这两个字段本来就是 100ns tick，直接取用，不再经过「秒/tick」猜测换算。
	p.PositionTicks = int64Value(sess[obfPositionTicks])
	p.RuntimeTicks = int64Value(sess[obfRuntimeTicks])
	if p.PositionTicks < 0 {
		p.PositionTicks = 0
	}
	if p.RuntimeTicks < 0 {
		p.RuntimeTicks = 0
	}

	// 媒体库条目 ID 是数字（例如 1524 / 135457 / 20663），可用于 host.item 补全和海报。
	p.ItemID = stringValue(sess[obfItemIDStr])
	if p.ItemID == "" {
		p.ItemID = numberString(sess[obfItemID])
	}
	if p.ItemID != "" {
		p.CandidateIDs = []string{p.ItemID}
	}
	// PlayedToCompletion 不取事件里的 bool（那是暂停标志），由停止时的进度阈值判定。
	return p, true
}
