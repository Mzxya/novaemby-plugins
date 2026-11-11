package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 某些 NovaEmby / 兼容服务端会把播放事件的字段名替换成随机字符串
// （例如 {"UQVNVII2":{"FQBAcs2Ewxg":"root",...}}），无法按文档结构解码。
// 这里退化为「按值形状识别」：从任意 JSON 里找出 ID、时间戳、用户名、设备、
// 客户端、片名和播放时长，并把所有 ID 作为候选交给 host.item 验证。
var (
	hex32Re     = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	hexLikeRe   = regexp.MustCompile(`^[0-9a-fA-F]{16,64}$`)
	guidRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	versionRe   = regexp.MustCompile(`^\d+(\.\d+)+$`)
	numericRe   = regexp.MustCompile(`^\d+$`)
	ipRe        = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(:\d+)?$`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
)

// 技术性字符串，不应当作片名或用户名。
var technicalLabels = map[string]bool{
	"directplay": true, "directstream": true, "transcode": true, "direct": true,
	"copy": true, "remux": true, "hls": true, "http": true, "https": true,
	"emby": true, "jellyfin": true, "infuse": true, "web": true, "android": true,
	"ios": true, "unknown": true, "none": true, "null": true, "true": true, "false": true,
	"playing": true, "paused": true, "stopped": true, "idle": true,
}

// 设备和客户端的名称通常是明文（只混淆键名，不混淆值），用词表把它们摘出来，
// 剩下的短文本才是用户名，较长的才是片名。
var deviceHints = []string{
	"iphone", "ipad", "ipod", "android", "chrome", "firefox", "safari", "edge",
	"windows", "macos", "macbook", "appletv", "apple tv", "smart tv", "linux",
	"browser", "playstation", "xbox", "nvidia shield",
	"手机", "平板", "电视", "电脑", "浏览器",
}

var clientHints = []string{
	"infuse", "emby", "jellyfin", "kodi", "vlc", "theater", "mpv", "plex", "player", "client",
}

type parsedEvent struct {
	SessionID          string
	UserID             string
	UserName           string
	ItemID             string
	ItemName           string
	ItemType           string
	DeviceName         string
	Client             string
	PlayMethod         string
	RemoteAddr         string
	PositionTicks      int64
	RuntimeTicks       int64
	PlayedToCompletion bool
	CandidateIDs       []string
	Source             string // "doc" 或 "shape"
}

// parsePlaybackEvent 先按文档结构解析；字段缺失时按值形状兜底。
func parsePlaybackEvent(data []byte) parsedEvent {
	var doc playbackEvent
	if err := json.Unmarshal(data, &doc); err == nil {
		name := strings.TrimSpace(doc.Session.UserName)
		if name != "" || doc.itemID() != "" || doc.sessionID() != "" {
			runtime := doc.Session.RuntimeTicks
			if runtime <= 0 {
				runtime = doc.RuntimeTicks
			}
			return parsedEvent{
				Source: "doc", SessionID: doc.sessionID(), UserID: doc.Session.UserID.String(),
				UserName: name, ItemID: doc.itemID(), ItemName: strings.TrimSpace(doc.ItemName),
				ItemType: doc.ItemType, DeviceName: doc.Session.DeviceName, Client: doc.Session.Client,
				PlayMethod: doc.Session.PlayMethod, RemoteAddr: doc.Session.RemoteAddr,
				PositionTicks: doc.Session.PositionTicks, RuntimeTicks: runtime, PlayedToCompletion: doc.completed(),
			}
		}
	}
	// 键名被固定混淆的服务端：优先按已确认的映射精确取值。
	if p, ok := parseKnownShape(data); ok {
		return p
	}
	return parseByShape(data)
}

type flatValue struct {
	path  []string
	value any
	depth int
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 固定顺序，保证同一事件每次解析结果一致
	return keys
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func flatten(v any, path []string, depth int, out *[]flatValue) {
	if depth > 8 {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			p := append(append([]string{}, path...), k)
			flatten(t[k], p, depth+1, out)
		}
	case []any:
		for i, e := range t {
			p := append(append([]string{}, path...), strconv.Itoa(i))
			flatten(e, p, depth+1, out)
		}
	default:
		*out = append(*out, flatValue{path: path, value: v, depth: depth})
	}
}

func normalizeItemType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "movie":
		return "Movie"
	case "episode":
		return "Episode"
	case "series":
		return "Series"
	case "season":
		return "Season"
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	case "musicvideo":
		return "MusicVideo"
	case "trailer":
		return "Trailer"
	}
	return ""
}

// toSeconds 把事件里的数字统一换算成秒：既接受秒，也接受 100ns tick 计数。
func toSeconds(n float64) float64 {
	if n <= 0 {
		return 0
	}
	if n >= 1e6 {
		s := n / float64(ticksPerSecond)
		if s > 0 && s <= 86400 {
			return s
		}
		return 0
	}
	if n <= 86400 {
		return n
	}
	return 0
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isNoiseLabel(s string) bool {
	return technicalLabels[strings.ToLower(s)] || looksLikePath(s) || normalizeItemType(s) != ""
}

func matchesHints(low string, hints []string) bool {
	for _, hint := range hints {
		if strings.Contains(low, hint) {
			return true
		}
	}
	return false
}

type idValue struct {
	value string
	depth int
	path  string
}

func parseByShape(data []byte) parsedEvent {
	p := parsedEvent{Source: "shape"}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return p
	}
	if asMap(root) == nil {
		return p
	}

	var all []flatValue
	flatten(root, nil, 0, &all)

	var guids, hexIDs, labels []flatValue
	var seconds []float64
	var remoteAddrs, playMethods []string
	boolTrue := false
	for _, fv := range all {
		switch t := fv.value.(type) {
		case string:
			str := strings.TrimSpace(t)
			switch {
			case str == "":
			case timestampRe.MatchString(str):
				// RFC3339 / ISO 日期是事件时间，绝不能当成片名或用户名。
			case guidRe.MatchString(str):
				guids = append(guids, fv)
			case hex32Re.MatchString(str), hexLikeRe.MatchString(str):
				hexIDs = append(hexIDs, fv)
			case numericRe.MatchString(str) && len(str) >= 4 && len(str) <= 20:
				// Emby 的条目 ID 经常是纯数字字符串，也要参与候选验证。
				hexIDs = append(hexIDs, fv)
			case ipRe.MatchString(str):
				remoteAddrs = append(remoteAddrs, str)
			case versionRe.MatchString(str), numericRe.MatchString(str):
			default:
				low := strings.ToLower(str)
				if low == "directplay" || low == "directstream" || low == "transcode" {
					playMethods = append(playMethods, str)
				}
				labels = append(labels, fv)
			}
		case float64:
			if s := toSeconds(t); s > 0 {
				seconds = append(seconds, s)
			}
		case json.Number:
			if n, err := t.Float64(); err == nil {
				if s := toSeconds(n); s > 0 {
					seconds = append(seconds, s)
				}
			}
		case bool:
			if t {
				boolTrue = true
			}
		}
	}
	if len(remoteAddrs) > 0 {
		p.RemoteAddr = remoteAddrs[0]
	}
	if len(playMethods) > 0 {
		p.PlayMethod = playMethods[0]
	}

	// 可读文本按路径排序，先摘出设备和客户端，剩下的短文本才是用户名。
	sort.SliceStable(labels, func(i, j int) bool {
		if labels[i].depth != labels[j].depth {
			return labels[i].depth < labels[j].depth
		}
		return strings.Join(labels[i].path, "/") < strings.Join(labels[j].path, "/")
	})
	ordered := make([]string, 0, len(labels))
	for _, fv := range labels {
		str := strings.TrimSpace(fv.value.(string))
		if isNoiseLabel(str) {
			continue
		}
		ordered = append(ordered, str)
	}
	rest := make([]string, 0, len(ordered))
	for _, str := range ordered {
		low := strings.ToLower(str)
		switch {
		case p.DeviceName == "" && matchesHints(low, deviceHints):
			p.DeviceName = str
		case p.Client == "" && matchesHints(low, clientHints):
			p.Client = str
		default:
			rest = append(rest, str)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if hasNonASCII(a) != hasNonASCII(b) {
			return !hasNonASCII(a) // 用户名多为短 ASCII，片名常含中文
		}
		return len([]rune(a)) < len([]rune(b))
	})
	if len(rest) > 0 {
		p.UserName = rest[0]
	}

	for _, fv := range labels {
		if typ := normalizeItemType(strings.TrimSpace(fv.value.(string))); typ != "" {
			p.ItemType = typ
		}
	}
	used := map[string]bool{}
	for _, v := range []string{p.UserName, p.DeviceName, p.Client} {
		if v != "" {
			used[v] = true
		}
	}
	// 片名：排除会话信息与噪声，中文标题优先；时间戳已在上一步被剔除。
	titles := make([]flatValue, 0)
	for _, fv := range labels {
		str := strings.TrimSpace(fv.value.(string))
		if used[str] || isNoiseLabel(str) || len([]rune(str)) > 120 {
			continue
		}
		titles = append(titles, fv)
	}
	sort.SliceStable(titles, func(i, j int) bool {
		a, b := titles[i], titles[j]
		if a.depth != b.depth {
			return a.depth > b.depth
		}
		sa, sb := strings.TrimSpace(a.value.(string)), strings.TrimSpace(b.value.(string))
		na, nb := hasNonASCII(sa), hasNonASCII(sb)
		if na != nb {
			return na
		}
		return len([]rune(sa)) > len([]rune(sb))
	})
	if len(titles) > 0 {
		p.ItemName = strings.TrimSpace(titles[0].value.(string))
	}

	// 所有 ID 作为候选：嵌套越深越可能是正在播放的条目 ID，优先用 host.item 验证。
	ids := make([]idValue, 0, len(guids)+len(hexIDs))
	for _, fv := range guids {
		ids = append(ids, idValue{strings.TrimSpace(fv.value.(string)), fv.depth, strings.Join(fv.path, "/")})
	}
	for _, fv := range hexIDs {
		ids = append(ids, idValue{strings.TrimSpace(fv.value.(string)), fv.depth, strings.Join(fv.path, "/")})
	}
	// 部分服务端只接受无连字符的 32 位十六进制条目 ID，额外补一份紧凑形式。
	for _, e := range append([]idValue{}, ids...) {
		if strings.Contains(e.value, "-") {
			compact := strings.ReplaceAll(e.value, "-", "")
			if hex32Re.MatchString(compact) {
				ids = append(ids, idValue{compact, e.depth, e.path + "/compact"})
			}
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if ids[i].depth != ids[j].depth {
			return ids[i].depth > ids[j].depth
		}
		return ids[i].path < ids[j].path
	})
	if len(ids) > 0 {
		p.ItemID = ids[0].value
	}
	if len(guids) > 0 { // 会话层的 GUID 用作合并 started / stopped 的键
		best := guids[0]
		for _, g := range guids[1:] {
			if g.depth < best.depth {
				best = g
			}
		}
		p.SessionID = strings.TrimSpace(best.value.(string))
	} else if len(hexIDs) > 1 {
		p.SessionID = strings.TrimSpace(hexIDs[1].value.(string))
	}
	if len(hexIDs) > 0 {
		p.UserID = strings.TrimSpace(hexIDs[0].value.(string))
	}
	seen := map[string]bool{}
	for _, e := range ids {
		if e.value != "" && !seen[e.value] && len(p.CandidateIDs) < 24 {
			seen[e.value] = true
			p.CandidateIDs = append(p.CandidateIDs, e.value)
		}
	}

	p.PlayedToCompletion = boolTrue
	sort.Slice(seconds, func(i, j int) bool { return seconds[i] > seconds[j] })
	if len(seconds) > 0 {
		p.RuntimeTicks = int64(seconds[0]) * ticksPerSecond
	}
	if len(seconds) > 1 {
		p.PositionTicks = int64(seconds[1]) * ticksPerSecond
	}
	return p
}
