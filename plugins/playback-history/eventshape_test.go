package main

import (
	"strings"
	"testing"
	"time"
)

// 字段名被混淆的事件：所有键都是随机字符串，只有值携带信息。
const obfuscatedEvent = `{"UQVNVII2":{
  "EUebnB":"8b0d1149f7b4b79611d09ced064fb55f",
  "Ma1NLTTGgLkj":"7ff081eec2b083eb7d3b8967c40cbb6d",
  "FQBAcs2Ewxg":"root",
  "Lp_fY7Tvs7":"BFD8AB74-4659-4C0B-AFB5-202500DED30F",
  "VapVfGw8id":"iPhone",
  "ZgySkQw":"Infuse-Direct",
  "Thafucf0N":"8.5.9",
  "P0aX1v":2591,
  "V3S9zRa":2591,
  "Sk2P5N":"e06f7c6848b2c4a1f9d3e5b7a0c1d2e3f",
  "Xk9Pq":{"Ab12":"流浪地球2","Cd34":"Movie","Ef56":"a1b2c3d4e5f60718293a4b5c6d7e8f90"}
}}`

func TestParseObfuscatedEvent(t *testing.T) {
	p := parsePlaybackEvent([]byte(obfuscatedEvent))
	if p.Source != "shape" {
		t.Fatalf("解析方式 = %s，应为 shape", p.Source)
	}
	if p.UserName != "root" || p.DeviceName != "iPhone" || p.Client != "Infuse-Direct" {
		t.Fatalf("会话信息识别错误：%+v", p)
	}
	if p.ItemName != "流浪地球2" || p.ItemType != "Movie" {
		t.Fatalf("片名/类型识别错误：%+v", p)
	}
	if p.SessionID != "BFD8AB74-4659-4C0B-AFB5-202500DED30F" {
		t.Fatalf("会话 ID 识别错误：%q", p.SessionID)
	}
	if !strings.Contains(strings.Join(p.CandidateIDs, ","), "a1b2c3d4e5f60718293a4b5c6d7e8f90") {
		t.Fatalf("候选条目 ID 未包含嵌套对象里的 ID：%v", p.CandidateIDs)
	}
	if p.RuntimeTicks <= 0 || p.PositionTicks <= 0 {
		t.Fatalf("播放时长/进度未识别：runtime=%d position=%d", p.RuntimeTicks, p.PositionTicks)
	}
}

// 扁平的混淆事件里没有片名，但必须仍然能解析出用户和会话，不能整条丢弃。
func TestParseFlatObfuscatedEvent(t *testing.T) {
	data := []byte(`{"UQ":{"A":"8b0d1149f7b4b79611d09ced064fb55f","B":"7ff081eec2b083eb7d3b8967c40cbb6d","C":"root","D":"BFD8AB74-4659-4C0B-AFB5-202500DED30F","E":"iPhone","F":"Infuse-Direct","G":"8.5.9","H":2591,"I":"DirectPlay","J":"192.168.1.20"}}`)
	p := parsePlaybackEvent(data)
	if p.Source != "shape" || p.UserName != "root" || p.SessionID == "" || len(p.CandidateIDs) == 0 {
		t.Fatalf("扁平混淆事件解析失败：%+v", p)
	}
	if p.DeviceName != "iPhone" || p.Client != "Infuse-Direct" {
		t.Fatalf("技术字段污染了设备/客户端识别：%+v", p)
	}
}

func TestParseDocumentedEventUnchanged(t *testing.T) {
	data := []byte(`{"Session":{"ID":"s1","UserID":"u1","UserName":"张三","ItemID":"100","PositionTicks":123456789},"ItemID":"100","ItemName":"测试电影","ItemType":"Movie"}`)
	p := parsePlaybackEvent(data)
	if p.Source != "doc" || p.UserName != "张三" || p.ItemID != "100" || p.ItemName != "测试电影" {
		t.Fatalf("文档结构解析被破坏：%+v", p)
	}
}

func TestParseBrokenEventNeverPanics(t *testing.T) {
	for _, data := range []string{"", "{", "null", "[]", `{"a":123}`} {
		p := parsePlaybackEvent([]byte(data))
		if p.Source != "shape" {
			t.Fatalf("非法数据 %q 解析方式异常：%s", data, p.Source)
		}
	}
}

// 端到端：字段名被混淆的 started / stopped 事件必须产生一条可查询的记录。
func TestObfuscatedEventProducesQueryableRecord(t *testing.T) {
	s := openTestStore(t)
	p := parsePlaybackEvent([]byte(obfuscatedEvent))
	if p.UserName == "" {
		p.UserName = "未知用户"
	}
	if p.ItemName == "" {
		p.ItemName = "未知条目"
	}
	at := time.Date(2026, 11, 7, 16, 35, 55, 0, time.UTC)
	rec := PlaybackRecord{
		SessionID: p.SessionID, UserID: p.UserID, UserName: p.UserName,
		ItemID: p.ItemID, ItemIDCandidates: p.CandidateIDs, ItemName: p.ItemName,
		ItemType: p.ItemType, StartedAt: at, LastEventAt: at,
		PositionTicks: p.PositionTicks, StartPositionTicks: p.PositionTicks,
		RuntimeTicks: p.RuntimeTicks, DeviceName: p.DeviceName, Client: p.Client,
	}
	id, _, err := s.Start(rec)
	if err != nil || id == "" {
		t.Fatalf("started 事件未落库：id=%q err=%v", id, err)
	}
	stopped := rec
	stopped.LastEventAt = at.Add(20 * time.Minute)
	stopID, err := s.Stop(stopped, 90)
	if err != nil {
		t.Fatal(err)
	}
	if stopID != id {
		t.Fatalf("started 与 stopped 未合并成同一条：%s != %s", id, stopID)
	}
	result := s.Query(Query{Page: 1, PageSize: 30})
	if result.Total != 1 {
		t.Fatalf("应查到 1 条记录，实际 %d", result.Total)
	}
	got := result.Records[0]
	if got.UserName != "root" || got.ItemName != "流浪地球2" || got.DeviceName != "iPhone" {
		t.Fatalf("记录内容不正确：%+v", got)
	}
}

// 真实事件里带 RFC3339 时间戳；时间戳不能抢占用户名和片名。
func TestParseObfuscatedEventWithTimestamps(t *testing.T) {
	data := []byte(`{"UQ":{
		"AAA":"2026-11-07T17:59:13.490141548+08:00",
		"BBB":"2026-11-07T17:59:09.183909663+08:00",
		"CCC":"8b0d1149f7b4b79611d09ced064fb55f",
		"DDD":"root",
		"EEE":"流浪地球2",
		"FFF":"BFD8AB74-4659-4C0B-AFB5-202500DED30F",
		"GGG":"iPhone",
		"HHH":"Infuse-Direct",
		"III":"8.5.9",
		"JJJ":"Movie",
		"KKK":"123456",
		"LLL":"DirectPlay",
		"MMM":"192.168.1.20",
		"NNN":2591,
		"OOO":2591
	}}`)
	p := parsePlaybackEvent(data)
	if p.UserName != "root" || p.ItemName != "流浪地球2" {
		t.Fatalf("时间戳污染了用户名/片名：user=%q item=%q", p.UserName, p.ItemName)
	}
	if p.DeviceName != "iPhone" || p.Client != "Infuse-Direct" {
		t.Fatalf("设备/客户端识别错误：device=%q client=%q", p.DeviceName, p.Client)
	}
	if p.ItemType != "Movie" || p.PlayMethod != "DirectPlay" || p.RemoteAddr != "192.168.1.20" {
		t.Fatalf("类型/播放方式/IP 识别错误：%+v", p)
	}
	if p.SessionID != "BFD8AB74-4659-4C0B-AFB5-202500DED30F" {
		t.Fatalf("会话 ID 错误：%q", p.SessionID)
	}
	joined := strings.Join(p.CandidateIDs, ",")
	if !strings.Contains(joined, "123456") || !strings.Contains(joined, "BFD8AB7446594C0BAFB5202500DED30F") {
		t.Fatalf("数字 ID 或紧凑 GUID 未进入候选：%v", p.CandidateIDs)
	}
	if p.RuntimeTicks <= 0 {
		t.Fatal("播放时长未识别")
	}
}
