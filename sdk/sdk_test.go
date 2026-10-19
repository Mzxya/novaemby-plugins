package sdk

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost 模拟 NovaEmby：通过管道和插件主循环交换消息。
type fakeHost struct {
	t      *testing.T
	toPlug *io.PipeWriter
	in     *bufio.Scanner
	mu     sync.Mutex
	lines  chan map[string]any
	done   chan int
}

func startPlugin(t *testing.T, p *Plugin) *fakeHost {
	pr, pw := io.Pipe() // 宿主 → 插件
	or, ow := io.Pipe() // 插件 → 宿主
	h := &fakeHost{t: t, toPlug: pw, lines: make(chan map[string]any, 100), done: make(chan int, 1)}
	go func() {
		h.done <- run(p, pr, ow)
		ow.Close()
	}()
	go func() {
		sc := bufio.NewScanner(or)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				h.lines <- m
			}
		}
		close(h.lines)
	}()
	t.Cleanup(func() { pw.Close() })
	return h
}

func (h *fakeHost) send(v map[string]any) {
	v["jsonrpc"] = "2.0"
	b, _ := json.Marshal(v)
	_, _ = h.toPlug.Write(append(b, '\n'))
}

// next 下一条不是 log 通知的消息。
func (h *fakeHost) next() map[string]any {
	h.t.Helper()
	for {
		select {
		case m, ok := <-h.lines:
			if !ok {
				h.t.Fatal("插件已退出")
			}
			if m["method"] == "log" {
				continue
			}
			return m
		case <-time.After(5 * time.Second):
			h.t.Fatal("等待插件消息超时")
		}
	}
}

func (h *fakeHost) init(config map[string]any) map[string]any {
	h.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"protocol": 1,
		"server": map[string]any{"id": "s1", "name": "Nova", "version": "1.0.0"},
		"plugin": map[string]any{"id": "demo", "version": "0.1.0", "dataDir": "/tmp/x"}, "config": config}})
	return h.next()
}

func TestRunLifecycle(t *testing.T) {
	var mu sync.Mutex
	var events []string
	var shut bool
	p := &Plugin{
		OnInit: func(h *Host) error {
			if h.Config().String("mode") == "bad" {
				return errors.New("模式不对")
			}
			h.Info("ready", "count", h.Config().Int("count"))
			return nil
		},
		OnConfigure: func(h *Host, cfg Config) error {
			if cfg.Int("count") > 5 {
				return errors.New("count 太大")
			}
			return nil
		},
		OnEvent: func(h *Host, ev Event) {
			mu.Lock()
			events = append(events, ev.Name)
			mu.Unlock()
		},
		Routes: map[string]HandlerFunc{
			"GET /api/x/{id}": func(h *Host, r *Request) *Response {
				return JSON(200, map[string]any{"id": r.Params["id"], "q": r.Query.Get("a"), "count": h.Config().Int("count"),
					"server": h.Server().Name, "user": r.User != nil})
			},
			"POST /api/body": func(h *Host, r *Request) *Response { return Text(201, strings.ToUpper(string(r.Body))) },
			"GET /api/item/{id}": func(h *Host, r *Request) *Response {
				it, err := h.Item(r.Context, r.Params["id"])
				if errors.Is(err, ErrNotFound) {
					return Status(404)
				}
				if err != nil {
					return Text(500, err.Error())
				}
				return JSON(200, it)
			},
			"GET /api/slow": func(h *Host, r *Request) *Response {
				<-r.Context.Done()
				return Text(499, "cancelled")
			},
			"GET /api/panic": func(h *Host, r *Request) *Response { panic("boom") },
			"GET /api/nil":   func(h *Host, r *Request) *Response { return nil },
		},
		OnShutdown: func(h *Host) { shut = true },
	}
	h := startPlugin(t, p)

	if m := h.init(map[string]any{"count": 3}); m["error"] != nil {
		t.Fatalf("初始化失败：%v", m)
	}

	// HTTP
	h.send(map[string]any{"id": 2, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/x/{id}",
		"params": map[string]string{"id": "42"}, "query": map[string][]string{"a": {"b"}}}})
	m := h.next()
	res := m["result"].(map[string]any)
	body, _ := base64.StdEncoding.DecodeString(res["body"].(string))
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if res["status"] != float64(200) || got["id"] != "42" || got["q"] != "b" || got["count"] != float64(3) || got["server"] != "Nova" {
		t.Fatalf("http 结果不对：%v %v", res, got)
	}
	// HEAD 用 GET 的处理函数
	h.send(map[string]any{"id": 3, "method": "http", "params": map[string]any{"method": "HEAD", "route": "/api/x/{id}"}})
	if m := h.next(); m["result"].(map[string]any)["status"] != float64(200) {
		t.Fatalf("HEAD：%v", m)
	}
	h.send(map[string]any{"id": 4, "method": "http", "params": map[string]any{"method": "POST", "route": "/api/body",
		"body": base64.StdEncoding.EncodeToString([]byte("hi"))}})
	res = h.next()["result"].(map[string]any)
	body, _ = base64.StdEncoding.DecodeString(res["body"].(string))
	if string(body) != "HI" || res["status"] != float64(201) {
		t.Fatalf("请求体：%v %s", res, body)
	}
	h.send(map[string]any{"id": 5, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/panic"}})
	if m := h.next(); m["error"] == nil {
		t.Fatal("panic 应返回错误而不是让插件崩溃")
	}
	h.send(map[string]any{"id": 6, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/nil"}})
	if m := h.next(); m["result"].(map[string]any)["status"] != float64(204) {
		t.Fatal("返回 nil 应为 204")
	}

	// 取消
	h.send(map[string]any{"id": 7, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/slow"}})
	time.Sleep(50 * time.Millisecond)
	h.send(map[string]any{"method": "$/cancel", "params": map[string]any{"id": 7}})
	if m := h.next(); m["result"].(map[string]any)["status"] != float64(499) {
		t.Fatalf("取消：%v", m)
	}

	// host.item：插件发请求，宿主回复
	h.send(map[string]any{"id": 8, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/item/{id}",
		"params": map[string]string{"id": "11"}}})
	req := h.next()
	if req["method"] != "host.item" {
		t.Fatalf("应向宿主请求条目：%v", req)
	}
	h.send(map[string]any{"id": req["id"], "result": map[string]any{"id": "11", "name": "第三集", "episodeNumber": 3}})
	res = h.next()["result"].(map[string]any)
	body, _ = base64.StdEncoding.DecodeString(res["body"].(string))
	var it Item
	_ = json.Unmarshal(body, &it)
	if it.Name != "第三集" || it.EpisodeNumber == nil || *it.EpisodeNumber != 3 {
		t.Fatalf("条目：%s", body)
	}
	h.send(map[string]any{"id": 9, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/item/{id}",
		"params": map[string]string{"id": "404"}}})
	req = h.next()
	h.send(map[string]any{"id": req["id"], "error": map[string]any{"code": 404, "message": "条目不存在"}})
	if m := h.next(); m["result"].(map[string]any)["status"] != float64(404) {
		t.Fatalf("条目不存在：%v", m)
	}

	// 配置
	h.send(map[string]any{"id": 10, "method": "configure", "params": map[string]any{"config": map[string]any{"count": 9}}})
	if m := h.next(); m["error"] == nil {
		t.Fatal("插件应拒绝 count 9")
	}
	h.send(map[string]any{"id": 11, "method": "configure", "params": map[string]any{"config": map[string]any{"count": 4}}})
	if m := h.next(); m["error"] != nil {
		t.Fatalf("配置：%v", m)
	}
	h.send(map[string]any{"id": 12, "method": "http", "params": map[string]any{"method": "GET", "route": "/api/x/{id}"}})
	res = h.next()["result"].(map[string]any)
	body, _ = base64.StdEncoding.DecodeString(res["body"].(string))
	_ = json.Unmarshal(body, &got)
	if got["count"] != float64(4) {
		t.Fatalf("新配置应生效：%v", got)
	}

	// 事件按顺序
	for _, n := range []string{"a.1", "a.2", "a.3"} {
		h.send(map[string]any{"method": "event", "params": map[string]any{"name": n, "at": 1, "data": map[string]any{}}})
	}
	h.send(map[string]any{"id": 13, "method": "ping"})
	h.next()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	if strings.Join(events, ",") != "a.1,a.2,a.3" {
		t.Fatalf("事件顺序：%v", events)
	}
	mu.Unlock()

	// 未知方法、shutdown
	h.send(map[string]any{"id": 14, "method": "nope"})
	if m := h.next(); m["error"] == nil {
		t.Fatal("未知方法应返回错误")
	}
	h.send(map[string]any{"id": 15, "method": "shutdown"})
	h.next()
	select {
	case code := <-h.done:
		if code != 0 || !shut {
			t.Fatalf("退出：%d %v", code, shut)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown 后应退出")
	}
}

func TestInitFailureAndEOF(t *testing.T) {
	h := startPlugin(t, &Plugin{OnInit: func(h *Host) error {
		if h.Config().String("mode") == "bad" {
			return errors.New("模式不对")
		}
		return nil
	}})
	m := h.init(map[string]any{"mode": "bad"})
	if e, _ := m["error"].(map[string]any); e == nil || e["message"] != "模式不对" {
		t.Fatalf("初始化失败应返回原因：%v", m)
	}
	h.toPlug.Close() // stdin 关闭：插件退出
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("stdin 关闭后应退出")
	}
}

func TestProtocolMismatch(t *testing.T) {
	h := startPlugin(t, &Plugin{})
	h.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"protocol": 2}})
	if m := h.next(); m["error"] == nil {
		t.Fatal("协议版本不一致应失败")
	}
}

func TestConfigHelpers(t *testing.T) {
	c := normalizeConfig(Config{"n": float64(7), "f": 1.5, "s": "x", "b": true})
	if c.Int("n") != 7 || c.String("s") != "x" || !c.Bool("b") || c.String("missing") != "" || c.Int("s") != 0 {
		t.Fatalf("配置读取：%v", c)
	}
	_ = context.Background()
}
