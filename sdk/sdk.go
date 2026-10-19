// Package sdk 是写 NovaEmby 插件的 Go 开发包。
//
// 插件是一个独立的程序，NovaEmby 启动它，并通过 stdin / stdout 交换 JSON-RPC 消息（插件协议 1）。
// 开发包处理全部通信细节：写插件时只需要填 Plugin 的几个函数，然后在 main 里调用 sdk.Run。
//
//	func main() {
//		sdk.Run(&sdk.Plugin{
//			OnInit: func(h *sdk.Host) error { h.Info("插件已启动"); return nil },
//			Routes: map[string]sdk.HandlerFunc{
//				"GET /api/hello/{name}": func(h *sdk.Host, r *sdk.Request) *sdk.Response {
//					return sdk.JSON(200, map[string]string{"message": "你好，" + r.Params["name"]})
//				},
//			},
//		})
//	}
//
// 注意：
//   - stdout 是通信通道。sdk.Run 会把 os.Stdout 换成 stderr，fmt.Println 之类的输出会进入插件日志，不会破坏通信。
//   - 事件按到达顺序在一个 goroutine 里依次处理；HTTP 请求并发处理（同时最多 16 个）。
//   - 插件的数据请写到 h.DataDir()（升级插件时保留，卸载时可以选择保留）。
//
// 完整说明见《NovaEmby插件开发教程》。
package sdk

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion 开发包实现的插件协议版本（manifest.json 的 protocol 要写这个值）。
const ProtocolVersion = 1

// Plugin 插件要实现的回调。都可以不填。
type Plugin struct {
	// OnInit NovaEmby 启动插件后调用一次（此时配置、服务器信息都已可用）。返回错误 = 插件启动失败，原因显示给管理员。
	OnInit func(h *Host) error
	// OnConfigure 管理员修改配置时调用（新配置已经按 manifest 校验过）。返回错误 = 拒绝这个配置，不保存，原因显示给管理员。
	// 返回 nil 后 h.Config() 就是新配置。
	OnConfigure func(h *Host, cfg Config) error
	// OnEvent 收到订阅的事件（manifest 的 events）。按到达顺序依次调用，不要在这里长时间阻塞。
	OnEvent func(h *Host, ev Event)
	// Routes HTTP 接口，键为 "方法 路径"，与 manifest 的 routes 一一对应，例如 "GET /api/danmu/{id}/raw"。
	Routes map[string]HandlerFunc
	// OnShutdown 插件停止前调用（停用、升级、NovaEmby 关闭）。最多 2 秒，之后进程会被结束。
	OnShutdown func(h *Host)
}

// HandlerFunc 处理一个 HTTP 请求。返回 nil 等于 204。
type HandlerFunc func(h *Host, r *Request) *Response

// ───── 宿主信息与能力 ─────

// ServerInfo NovaEmby 服务器信息。
type ServerInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	EmbyVersion string `json:"embyVersion"` // 对 Emby 客户端报告的版本
}

// Host 插件与 NovaEmby 交互的入口。所有方法都可以并发调用。
type Host struct {
	conn *conn

	mu       sync.RWMutex
	config   Config
	server   ServerInfo
	pluginID string
	version  string
	dataDir  string
	proxy    string
	language string
	timeZone string
}

// Config 当前配置（只读副本）。
func (h *Host) Config() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config.clone()
}

func (h *Host) Server() ServerInfo { h.mu.RLock(); defer h.mu.RUnlock(); return h.server }
func (h *Host) PluginID() string   { h.mu.RLock(); defer h.mu.RUnlock(); return h.pluginID }
func (h *Host) Version() string    { h.mu.RLock(); defer h.mu.RUnlock(); return h.version }

// DataDir 插件自己的数据目录（可读写，升级后保留）。
func (h *Host) DataDir() string { h.mu.RLock(); defer h.mu.RUnlock(); return h.dataDir }

// Proxy NovaEmby 设置的代理（空 = 不用）。环境变量 HTTP_PROXY / HTTPS_PROXY 也已设置，http.DefaultClient 会自动使用。
func (h *Host) Proxy() string    { h.mu.RLock(); defer h.mu.RUnlock(); return h.proxy }
func (h *Host) Language() string { h.mu.RLock(); defer h.mu.RUnlock(); return h.language }
func (h *Host) TimeZone() string { h.mu.RLock(); defer h.mu.RUnlock(); return h.timeZone }

// Debug / Info / Warn / Error 写插件日志（在 NovaEmby 的插件日志中查看）。kv 为键值对：h.Info("下载完成", "file", name, "ms", 120)。
func (h *Host) Debug(msg string, kv ...any) { h.log("debug", msg, kv) }
func (h *Host) Info(msg string, kv ...any)  { h.log("info", msg, kv) }
func (h *Host) Warn(msg string, kv ...any)  { h.log("warn", msg, kv) }
func (h *Host) Error(msg string, kv ...any) { h.log("error", msg, kv) }

func (h *Host) log(level, msg string, kv []any) {
	fields := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		fields[fmt.Sprint(kv[i])] = kv[i+1]
	}
	if len(kv)%2 == 1 {
		fields["extra"] = kv[len(kv)-1]
	}
	p := map[string]any{"level": level, "msg": msg}
	if len(fields) > 0 {
		p["fields"] = fields
	}
	h.conn.notify("log", p)
}

// Item 条目的基本信息（host.item）。
type Item struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"` // Movie / Series / Season / Episode / Video …
	Name             string            `json:"name"`
	OriginalTitle    string            `json:"originalTitle"`
	Year             int               `json:"year"`
	PremiereDate     int64             `json:"premiereDate"` // Unix 秒，0 = 未知
	LibraryID        string            `json:"libraryId"`
	Path             string            `json:"path"`
	SeriesID         string            `json:"seriesId"`
	SeriesName       string            `json:"seriesName"`
	SeasonID         string            `json:"seasonId"`
	SeasonNumber     *int              `json:"seasonNumber"`
	EpisodeNumber    *int              `json:"episodeNumber"`
	EpisodeNumberEnd *int              `json:"episodeNumberEnd"`
	RuntimeTicks     int64             `json:"runtimeTicks"`
	ProviderIDs      map[string]string `json:"providerIds"` // Tmdb / Imdb / Tvdb / Douban
}

// ErrNotFound 条目不存在。
var ErrNotFound = errors.New("not found")

// Item 读取条目信息。不存在返回 ErrNotFound。
func (h *Host) Item(ctx context.Context, itemID string) (*Item, error) {
	var it Item
	err := h.conn.call(ctx, "host.item", map[string]string{"itemId": itemID}, &it)
	var re *RPCError
	if errors.As(err, &re) && re.Code == 404 {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// Activity 写一条活动记录（NovaEmby 控制台的活动日志）。severity：info / warn / error。每分钟最多 30 条。
func (h *Host) Activity(ctx context.Context, name, overview, severity string) error {
	return h.conn.call(ctx, "host.activity", map[string]string{"name": name, "overview": overview, "severity": severity}, nil)
}

// ───── 配置 ─────

// Config 配置值（键为 manifest 中的 key）。数字为 int64，开关为 bool，其余为 string。
type Config map[string]any

func (c Config) clone() Config {
	out := Config{}
	for k, v := range c {
		out[k] = v
	}
	return out
}

func (c Config) String(key string) string {
	switch v := c[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (c Config) Int(key string) int64 {
	switch v := c[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func (c Config) Bool(key string) bool {
	b, _ := c[key].(bool)
	return b
}

// ───── 事件 ─────

// Event 一个事件。Data 是 NovaEmby 中事件结构的 JSON（字段名 PascalCase），用 Decode 解到自己的结构体。
type Event struct {
	Name string          `json:"name"`
	At   time.Time       `json:"-"`
	Data json.RawMessage `json:"data"`
}

func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// ───── HTTP ─────

// User 发起请求的用户（没有有效凭据时 Request.User 为 nil）。
type User struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsAdmin    bool   `json:"isAdmin"`
	IsAPIKey   bool   `json:"isApiKey"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Client     string `json:"client"`
}

// Request 转发过来的 HTTP 请求。凭据（Authorization、Cookie、X-Emby-Token、api_key 等）已经去掉。
type Request struct {
	Context      context.Context // 客户端断开或超时时取消
	Method       string
	Path         string // 去掉 /emby 前缀后的路径
	OriginalPath string
	Route        string            // manifest 中的路径，例如 /api/danmu/{id}/raw
	Params       map[string]string // 路径参数
	Query        url.Values
	Header       http.Header
	Body         []byte
	RemoteAddr   string
	User         *User
}

// Response 返回给客户端的响应。
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON 返回 JSON。
func JSON(status int, v any) *Response {
	b, err := json.Marshal(v)
	if err != nil {
		return Text(http.StatusInternalServerError, "encode error")
	}
	return Bytes(status, "application/json; charset=utf-8", b)
}

// Text 返回纯文本。
func Text(status int, s string) *Response {
	return Bytes(status, "text/plain; charset=utf-8", []byte(s))
}

// Bytes 返回任意内容（例如 XML、图片）。
func Bytes(status int, contentType string, b []byte) *Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &Response{Status: status, Header: h, Body: b}
}

// Status 只返回状态码。
func Status(code int) *Response { return &Response{Status: code} }

// ───── 运行 ─────

// Run 运行插件，直到 NovaEmby 要求停止。不会返回（结束时直接退出进程）。
func Run(p *Plugin) {
	out := os.Stdout
	os.Stdout = os.Stderr // 防止误打印破坏通信
	code := run(p, os.Stdin, out)
	os.Exit(code)
}

// run 可测试的主循环：返回退出码。
func run(p *Plugin, in io.Reader, out io.Writer) int {
	c := newConn(out)
	h := &Host{conn: c, config: Config{}}
	rt := &runtime{p: p, h: h, c: c, cancels: map[string]context.CancelFunc{}, events: make(chan Event, 1024),
		httpSem: make(chan struct{}, 16)}
	go rt.eventLoop()
	return rt.readLoop(in)
}

type runtime struct {
	p       *Plugin
	h       *Host
	c       *conn
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	events  chan Event
	httpSem chan struct{}
	inited  atomic.Bool
	stopped atomic.Bool
}

func (rt *runtime) readLoop(in io.Reader) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m.Method == "" && m.ID != nil {
			rt.c.deliver(m)
			continue
		}
		if stop := rt.dispatch(m); stop {
			return 0
		}
	}
	// stdin 关闭：NovaEmby 要求退出或已经退出
	rt.shutdown()
	return 0
}

func (rt *runtime) dispatch(m message) bool {
	switch m.Method {
	case "initialize":
		var p struct {
			Protocol int        `json:"protocol"`
			Server   ServerInfo `json:"server"`
			Plugin   struct {
				ID      string `json:"id"`
				Version string `json:"version"`
				DataDir string `json:"dataDir"`
			} `json:"plugin"`
			Config   Config `json:"config"`
			Proxy    string `json:"proxy"`
			Language string `json:"language"`
			TimeZone string `json:"timeZone"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "initialize 参数不正确"})
			return false
		}
		if p.Protocol != ProtocolVersion {
			rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: fmt.Sprintf("插件开发包只支持协议 %d，NovaEmby 使用协议 %d", ProtocolVersion, p.Protocol)})
			return false
		}
		h := rt.h
		h.mu.Lock()
		h.server, h.pluginID, h.version, h.dataDir = p.Server, p.Plugin.ID, p.Plugin.Version, p.Plugin.DataDir
		h.config, h.proxy, h.language, h.timeZone = normalizeConfig(p.Config), p.Proxy, p.Language, p.TimeZone
		h.mu.Unlock()
		go func() {
			if rt.p.OnInit != nil {
				if err := safeErr(func() error { return rt.p.OnInit(h) }); err != nil {
					rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: err.Error()})
					return
				}
			}
			rt.inited.Store(true)
			rt.c.reply(m.ID, struct{}{}, nil)
		}()
	case "ping":
		rt.c.reply(m.ID, struct{}{}, nil)
	case "configure":
		var p struct {
			Config Config `json:"config"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "configure 参数不正确"})
			return false
		}
		cfg := normalizeConfig(p.Config)
		go func() {
			if rt.p.OnConfigure != nil {
				if err := safeErr(func() error { return rt.p.OnConfigure(rt.h, cfg.clone()) }); err != nil {
					rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: err.Error()})
					return
				}
			}
			rt.h.mu.Lock()
			rt.h.config = cfg
			rt.h.mu.Unlock()
			rt.c.reply(m.ID, struct{}{}, nil)
		}()
	case "event":
		var ev struct {
			Name string          `json:"name"`
			At   int64           `json:"at"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(m.Params, &ev) == nil {
			select {
			case rt.events <- Event{Name: ev.Name, At: time.Unix(ev.At, 0), Data: ev.Data}:
			default: // 处理太慢：丢弃（宿主那边也会计数）
			}
		}
	case "http":
		rt.handleHTTP(m)
	case "$/cancel":
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			rt.mu.Lock()
			if cancel := rt.cancels[string(p.ID)]; cancel != nil {
				cancel()
			}
			rt.mu.Unlock()
		}
	case "shutdown":
		rt.shutdown()
		rt.c.reply(m.ID, struct{}{}, nil)
		return true
	default:
		if m.ID != nil {
			rt.c.reply(m.ID, nil, &RPCError{Code: -32601, Message: "插件不支持方法 " + m.Method})
		}
	}
	return false
}

func (rt *runtime) shutdown() {
	if !rt.stopped.CompareAndSwap(false, true) {
		return
	}
	if rt.p.OnShutdown != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = safeErr(func() error { rt.p.OnShutdown(rt.h); return nil })
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (rt *runtime) eventLoop() {
	for ev := range rt.events {
		if rt.p.OnEvent == nil {
			continue
		}
		_ = safeErr(func() error { rt.p.OnEvent(rt.h, ev); return nil })
	}
}

type httpParams struct {
	Method       string              `json:"method"`
	Path         string              `json:"path"`
	OriginalPath string              `json:"originalPath"`
	Route        string              `json:"route"`
	Params       map[string]string   `json:"params"`
	Query        map[string][]string `json:"query"`
	Headers      map[string][]string `json:"headers"`
	Body         string              `json:"body"`
	RemoteAddr   string              `json:"remoteAddr"`
	User         *User               `json:"user"`
}

func (rt *runtime) handleHTTP(m message) {
	var p httpParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "http 参数不正确"})
		return
	}
	key := strings.ToUpper(p.Method) + " " + p.Route
	handler := rt.p.Routes[key]
	if handler == nil && strings.EqualFold(p.Method, http.MethodHead) {
		handler = rt.p.Routes["GET "+p.Route]
	}
	if handler == nil {
		rt.c.reply(m.ID, encodeResponse(Text(http.StatusNotFound, "Not found.")), nil)
		return
	}
	body, err := base64.StdEncoding.DecodeString(p.Body)
	if err != nil {
		rt.c.reply(m.ID, nil, &RPCError{Code: -32602, Message: "请求体不正确"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	idKey := string(*m.ID)
	rt.mu.Lock()
	rt.cancels[idKey] = cancel
	rt.mu.Unlock()
	req := &Request{Context: ctx, Method: p.Method, Path: p.Path, OriginalPath: p.OriginalPath, Route: p.Route,
		Params: p.Params, Query: url.Values(p.Query), Header: http.Header(p.Headers), Body: body, RemoteAddr: p.RemoteAddr, User: p.User}
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	if req.Header == nil {
		req.Header = http.Header{}
	}
	go func() {
		rt.httpSem <- struct{}{}
		defer func() {
			<-rt.httpSem
			cancel()
			rt.mu.Lock()
			delete(rt.cancels, idKey)
			rt.mu.Unlock()
		}()
		var resp *Response
		err := safeErr(func() error { resp = handler(rt.h, req); return nil })
		if err != nil {
			rt.h.Error("处理请求时出错", "route", key, "error", err.Error())
			rt.c.reply(m.ID, nil, &RPCError{Code: 1, Message: err.Error()})
			return
		}
		if resp == nil {
			resp = Status(http.StatusNoContent)
		}
		rt.c.reply(m.ID, encodeResponse(resp), nil)
	}()
}

func encodeResponse(r *Response) map[string]any {
	out := map[string]any{"status": r.Status}
	if len(r.Header) > 0 {
		out["headers"] = map[string][]string(r.Header)
	}
	if len(r.Body) > 0 {
		out["body"] = base64.StdEncoding.EncodeToString(r.Body)
	}
	return out
}

// normalizeConfig JSON 中的整数解码为 float64，这里统一换成 int64。
func normalizeConfig(c Config) Config {
	out := Config{}
	for k, v := range c {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			out[k] = int64(f)
			continue
		}
		out[k] = v
	}
	return out
}

func safeErr(f func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return f()
}

// ───── JSON-RPC ─────

type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError NovaEmby 返回的错误。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

type conn struct {
	mu      sync.Mutex
	w       io.Writer
	nextID  atomic.Int64
	pmu     sync.Mutex
	pending map[int64]chan message
}

func newConn(w io.Writer) *conn { return &conn{w: w, pending: map[int64]chan message{}} }

func (c *conn) send(m message) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.mu.Lock()
	_, _ = c.w.Write(append(b, '\n'))
	c.mu.Unlock()
}

func (c *conn) reply(id *json.RawMessage, result any, rerr *RPCError) {
	if id == nil {
		return
	}
	m := message{ID: id, Error: rerr}
	if rerr == nil {
		b, err := json.Marshal(result)
		if err != nil {
			m.Error = &RPCError{Code: -32603, Message: "结果无法编码"}
		} else {
			m.Result = b
		}
	}
	c.send(m)
}

func (c *conn) notify(method string, params any) {
	b, err := json.Marshal(params)
	if err != nil {
		return
	}
	c.send(message{Method: method, Params: b})
}

func (c *conn) call(ctx context.Context, method string, params any, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan message, 1)
	c.pmu.Lock()
	c.pending[id] = ch
	c.pmu.Unlock()
	raw := json.RawMessage(strconv.FormatInt(id, 10))
	c.send(message{ID: &raw, Method: method, Params: b})
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return ctx.Err()
	}
}

func (c *conn) deliver(m message) {
	id, err := strconv.ParseInt(string(*m.ID), 10, 64)
	if err != nil {
		return
	}
	c.pmu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.pmu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// SortedKeys 小工具：配置等 map 的有序键（写日志时用）。
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
