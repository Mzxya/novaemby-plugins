package main

import (
	"testing"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

// NovaEmby 下发的配置可能缺少某些键（例如从未保存过配置），
// 此时必须回退到 manifest 声明的默认值，而不是当作 false/0。
func TestConfigDefaults(t *testing.T) {
	cfg := sdk.Config{}
	if !configBool(cfg, "recordMovies", defaultRecordMovies) {
		t.Fatal("缺少配置键时应使用默认值 true")
	}
	if got := configInt(cfg, "retentionDays", defaultRetentionDays); got != 365 {
		t.Fatalf("缺少配置键时应使用默认值 365， got %d", got)
	}
	cfg["recordMovies"] = false
	if configBool(cfg, "recordMovies", defaultRecordMovies) {
		t.Fatal("显式设置为 false 时应生效")
	}
	cfg["retentionDays"] = int64(30)
	if got := configInt(cfg, "retentionDays", defaultRetentionDays); got != 30 {
		t.Fatalf("显式设置应生效， got %d", got)
	}
	cfg["maxRecords"] = float64(1200)
	if got := configInt(cfg, "maxRecords", defaultMaxRecords); got != 1200 {
		t.Fatalf("float64 配置应正确转换， got %d", got)
	}
}

func TestValidateConfig(t *testing.T) {
	for _, cfg := range []sdk.Config{
		{}, {"retentionDays": 0, "maxRecords": 1000, "completionPercent": 50},
		{"retentionDays": float64(3650), "maxRecords": int64(500000), "completionPercent": 100},
	} {
		if err := validateConfig(cfg); err != nil {
			t.Fatalf("有效配置被拒绝：%v", err)
		}
	}
	for _, cfg := range []sdk.Config{
		{"maxRecords": 0}, {"maxRecords": 500001}, {"maxRecords": 999},
		{"retentionDays": -1}, {"retentionDays": 3651}, {"completionPercent": 49},
		{"completionPercent": 101}, {"retentionDays": 1.5}, {"maxRecords": "50000"},
	} {
		if err := validateConfig(cfg); err == nil {
			t.Fatalf("无效配置被接受：%v", cfg)
		}
	}
}
