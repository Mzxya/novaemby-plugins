// mkrepo 打包插件、维护仓库清单 index.json、签名。GitHub Actions 用它发布插件；本地有 Go 时也可以手动运行。
//
//	go run ./tools/mkrepo info   -plugin plugins/hello                 # 输出 "id 版本"
//	go run ./tools/mkrepo check  -plugin plugins/hello                 # 检查 manifest.json
//	go run ./tools/mkrepo build  -plugin plugins/hello -out dist       # 编译 amd64 / arm64 并打包成 dist/hello-0.1.0.zip
//	go run ./tools/mkrepo has    -index index.json -id hello -version 0.1.0   # 清单里已有这个版本时退出码 0，否则 1
//	go run ./tools/mkrepo add    -index index.json -zip dist/hello-0.1.0.zip -url https://…/hello-0.1.0.zip [-changelog 文本]
//	go run ./tools/mkrepo sign   -index index.json                     # 用环境变量 NOVA_PLUGIN_SIGN_KEY（私钥 PEM）签名，写 index.json.sig
//	go run ./tools/mkrepo verify -index index.json [-pub base64公钥]     # 验证签名（不填 -pub 时用 NOVA_PLUGIN_SIGN_KEY 的私钥求出公钥）
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const protocolVersion = 1

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "info":
		err = cmdInfo(args)
	case "check":
		err = cmdCheck(args)
	case "build":
		err = cmdBuild(args)
	case "has":
		err = cmdHas(args)
	case "add":
		err = cmdAdd(args)
	case "sign":
		err = cmdSign(args)
	case "verify":
		err = cmdVerify(args)
	default:
		usage()
	}
	if err != nil {
		var ne notFound
		if errors.As(err, &ne) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：mkrepo info|check|build|has|add|sign|verify [参数]，见源码开头的说明")
	os.Exit(2)
}

type notFound struct{}

func (notFound) Error() string { return "not found" }

// ───── manifest ─────

type manifest struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Protocol         int               `json:"protocol"`
	Description      string            `json:"description"`
	Author           string            `json:"author"`
	Homepage         string            `json:"homepage"`
	MinServerVersion string            `json:"minServerVersion"`
	Entry            map[string]string `json:"entry"`
	Events           []string          `json:"events"`
	Routes           []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Auth   string `json:"auth"`
	} `json:"routes"`
	Config []struct {
		Key  string `json:"key"`
		Type string `json:"type"`
	} `json:"config"`
}

var (
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	platRe   = regexp.MustCompile(`^linux/(amd64|arm64)$`)
	keyRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

func readManifest(dir string) (*manifest, []byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("读取 %s/manifest.json：%w", dir, err)
	}
	m, err := parseManifest(b)
	return m, b, err
}

func parseManifest(b []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest.json 不是有效的 JSON：%w", err)
	}
	// 与 NovaEmby 的校验规则一致（主要部分）；完整规则以 NovaEmby 为准，上传安装时会再检查一次
	switch {
	case !idRe.MatchString(m.ID):
		return nil, fmt.Errorf("id %q 不合法：3–40 个字符，小写字母开头，只能有小写字母、数字和 -", m.ID)
	case !semverRe.MatchString(m.Version):
		return nil, fmt.Errorf("version %q 不是有效的版本号（例如 1.2.3）", m.Version)
	case m.Protocol != protocolVersion:
		return nil, fmt.Errorf("protocol 应为 %d", protocolVersion)
	case strings.TrimSpace(m.Name) == "":
		return nil, errors.New("缺少 name")
	case len(m.Entry) == 0:
		return nil, errors.New("缺少 entry")
	}
	for plat, p := range m.Entry {
		if !platRe.MatchString(plat) {
			return nil, fmt.Errorf("entry 平台 %q 不支持（linux/amd64、linux/arm64）", plat)
		}
		c := path.Clean(p)
		if p == "" || strings.HasPrefix(p, "/") || c == "." || strings.HasPrefix(c, "..") {
			return nil, fmt.Errorf("entry[%s] 必须是包内的相对路径", plat)
		}
	}
	for _, r := range m.Routes {
		low := strings.ToLower(r.Path)
		if !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(low, "/api/v1") || strings.HasPrefix(low, "/emby/") {
			return nil, fmt.Errorf("routes 的 path %q 不合法（以 / 开头，不能在 /api/v1 下，不要带 /emby 前缀）", r.Path)
		}
	}
	for _, c := range m.Config {
		if !keyRe.MatchString(c.Key) {
			return nil, fmt.Errorf("配置项 key %q 不合法", c.Key)
		}
		switch c.Type {
		case "string", "text", "secret", "int", "bool", "select":
		default:
			return nil, fmt.Errorf("配置项 %s 的 type %q 不支持", c.Key, c.Type)
		}
	}
	return &m, nil
}

func cmdInfo(args []string) error {
	fsx := flag.NewFlagSet("info", flag.ExitOnError)
	dir := fsx.String("plugin", "", "插件目录")
	fsx.Parse(args)
	m, _, err := readManifest(*dir)
	if err != nil {
		return err
	}
	fmt.Println(m.ID, m.Version)
	return nil
}

func cmdCheck(args []string) error {
	fsx := flag.NewFlagSet("check", flag.ExitOnError)
	dir := fsx.String("plugin", "", "插件目录")
	fsx.Parse(args)
	m, _, err := readManifest(*dir)
	if err != nil {
		return err
	}
	if base := filepath.Base(filepath.Clean(*dir)); base != m.ID {
		return fmt.Errorf("插件目录名（%s）应与 id（%s）相同", base, m.ID)
	}
	fmt.Printf("%s %s：manifest 正确\n", m.ID, m.Version)
	return nil
}

// ───── 编译打包 ─────

func cmdBuild(args []string) error {
	fsx := flag.NewFlagSet("build", flag.ExitOnError)
	dir := fsx.String("plugin", "", "插件目录")
	out := fsx.String("out", "dist", "输出目录")
	fsx.Parse(args)
	m, mb, err := readManifest(*dir)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "mkrepo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	plats := make([]string, 0, len(m.Entry))
	for p := range m.Entry {
		plats = append(plats, p)
	}
	sort.Strings(plats)
	for _, plat := range plats {
		goos, goarch, _ := strings.Cut(plat, "/")
		dst := filepath.Join(work, filepath.FromSlash(path.Clean(m.Entry[plat])))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", dst, "./"+filepath.ToSlash(filepath.Clean(*dir)))
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("编译 %s 失败：%w", plat, err)
		}
		fmt.Fprintf(os.Stderr, "已编译 %s\n", plat)
	}
	// 插件目录下的 assets/ 原样放进包里（图片、模板等）
	assets := filepath.Join(*dir, "assets")
	if st, err := os.Stat(assets); err == nil && st.IsDir() {
		if err := copyDir(assets, filepath.Join(work, "assets")); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(work, "manifest.json"), mb, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(*out, fmt.Sprintf("%s-%s.zip", m.ID, m.Version))
	if err := zipDir(work, zipPath, m); err != nil {
		return err
	}
	sum, size, err := hashFile(zipPath)
	if err != nil {
		return err
	}
	fmt.Println(zipPath, sum, size)
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("assets 中不能有符号链接：%s", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

// zipDir 打包（文件按路径排序、时间固定，同样的输入得到同样的 zip）。
func zipDir(dir, zipPath string, m *manifest) error {
	var files []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		return err
	}
	sort.Strings(files)
	exe := map[string]bool{}
	for _, e := range m.Entry {
		exe[path.Clean(e)] = true
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range files {
		fh := &zip.FileHeader{Name: f, Method: zip.Deflate, Modified: fixed}
		mode := os.FileMode(0o644)
		if exe[f] {
			mode = 0o755
		}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(zipPath, buf.Bytes(), 0o644)
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ───── 仓库清单 ─────

type index struct {
	FormatVersion int          `json:"formatVersion"`
	Name          string       `json:"name"`
	Plugins       []indexEntry `json:"plugins"`
}

type indexEntry struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Author      string         `json:"author,omitempty"`
	Homepage    string         `json:"homepage,omitempty"`
	Versions    []indexVersion `json:"versions"`
}

type indexVersion struct {
	Version          string   `json:"version"`
	Protocol         int      `json:"protocol"`
	MinServerVersion string   `json:"minServerVersion,omitempty"`
	Platforms        []string `json:"platforms,omitempty"`
	URL              string   `json:"url"`
	SHA256           string   `json:"sha256"`
	Size             int64    `json:"size"`
	Changelog        string   `json:"changelog,omitempty"`
	ReleasedAt       int64    `json:"releasedAt,omitempty"`
}

// defaultIndexName 新建清单时的仓库名称：官方仓库为“NovaEmby 官方插件”，其他仓库按 GitHub 仓库名
// （Actions 中的环境变量 GITHUB_REPOSITORY），也可以用环境变量 NOVA_REPO_NAME 指定。
func defaultIndexName() string {
	if n := strings.TrimSpace(os.Getenv("NOVA_REPO_NAME")); n != "" {
		return n
	}
	repo := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))
	if repo == "" || strings.EqualFold(repo, "Mzxya/novaemby-plugins") {
		return "NovaEmby 官方插件"
	}
	return repo + " 的插件"
}

func loadIndex(p string) (*index, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &index{FormatVersion: 1, Name: defaultIndexName(), Plugins: []indexEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("%s 不是有效的 JSON：%w", p, err)
	}
	return &idx, nil
}

func saveIndex(p string, idx *index) error {
	sort.Slice(idx.Plugins, func(i, j int) bool { return idx.Plugins[i].ID < idx.Plugins[j].ID })
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

func cmdHas(args []string) error {
	fsx := flag.NewFlagSet("has", flag.ExitOnError)
	p := fsx.String("index", "index.json", "仓库清单")
	id := fsx.String("id", "", "插件 ID")
	ver := fsx.String("version", "", "版本")
	fsx.Parse(args)
	idx, err := loadIndex(*p)
	if err != nil {
		return err
	}
	for _, e := range idx.Plugins {
		if e.ID != *id {
			continue
		}
		for _, v := range e.Versions {
			if v.Version == *ver {
				return nil
			}
		}
	}
	return notFound{}
}

func cmdAdd(args []string) error {
	fsx := flag.NewFlagSet("add", flag.ExitOnError)
	p := fsx.String("index", "index.json", "仓库清单")
	zipPath := fsx.String("zip", "", "插件包")
	url := fsx.String("url", "", "插件包的下载地址（绝对地址，或相对清单的路径）")
	changelog := fsx.String("changelog", "", "更新说明")
	fsx.Parse(args)
	if *zipPath == "" || *url == "" {
		return errors.New("需要 -zip 和 -url")
	}
	m, err := zipManifest(*zipPath)
	if err != nil {
		return err
	}
	sum, size, err := hashFile(*zipPath)
	if err != nil {
		return err
	}
	idx, err := loadIndex(*p)
	if err != nil {
		return err
	}
	plats := make([]string, 0, len(m.Entry))
	for k := range m.Entry {
		plats = append(plats, k)
	}
	sort.Strings(plats)
	v := indexVersion{Version: m.Version, Protocol: m.Protocol, MinServerVersion: m.MinServerVersion, Platforms: plats, URL: *url,
		SHA256: sum, Size: size, Changelog: strings.TrimSpace(*changelog), ReleasedAt: time.Now().Unix()}
	found := false
	for i := range idx.Plugins {
		e := &idx.Plugins[i]
		if e.ID != m.ID {
			continue
		}
		found = true
		e.Name, e.Description, e.Author, e.Homepage = m.Name, m.Description, m.Author, m.Homepage
		replaced := false
		for j := range e.Versions {
			if e.Versions[j].Version == m.Version {
				e.Versions[j], replaced = v, true
			}
		}
		if !replaced {
			e.Versions = append(e.Versions, v)
		}
		sort.Slice(e.Versions, func(a, b int) bool { return compareVersions(e.Versions[a].Version, e.Versions[b].Version) > 0 })
	}
	if !found {
		idx.Plugins = append(idx.Plugins, indexEntry{ID: m.ID, Name: m.Name, Description: m.Description, Author: m.Author,
			Homepage: m.Homepage, Versions: []indexVersion{v}})
	}
	if err := saveIndex(*p, idx); err != nil {
		return err
	}
	fmt.Printf("已加入 %s %s（%d 字节）\n", m.ID, m.Version, size)
	return nil
}

func zipManifest(p string) (*manifest, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			return parseManifest(b)
		}
	}
	return nil, errors.New("包里没有 manifest.json")
}

// compareVersions 语义化版本比较（预发布版低于正式版）。
func compareVersions(a, b string) int {
	pa, pb := splitVer(a), splitVer(b)
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	}
	return strings.Compare(pa.pre, pb.pre)
}

type ver struct {
	n   [3]int
	pre string
}

func splitVer(s string) ver {
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	var v ver
	for i, p := range strings.SplitN(core, ".", 3) {
		v.n[i], _ = strconv.Atoi(p)
	}
	v.pre = pre
	return v
}

// ───── 签名 ─────

func cmdSign(args []string) error {
	fsx := flag.NewFlagSet("sign", flag.ExitOnError)
	p := fsx.String("index", "index.json", "仓库清单")
	keyFile := fsx.String("key", "", "私钥 PEM 文件（不填则读环境变量 NOVA_PLUGIN_SIGN_KEY）")
	fsx.Parse(args)
	priv, err := loadPrivateKey(*keyFile)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*p)
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))
	if err := os.WriteFile(*p+".sig", []byte(sig+"\n"), 0o644); err != nil {
		return err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	fmt.Printf("已签名 %s（公钥 %s）\n", *p, pub)
	fmt.Println("只有官方仓库的签名（公钥内置在 NovaEmby 中）会被认作官方签名；其他仓库的签名按未签名处理")
	return nil
}

// loadPrivateKey 读取 ed25519 私钥（PKCS#8 PEM）：keyFile 为空时读环境变量 NOVA_PLUGIN_SIGN_KEY。
func loadPrivateKey(keyFile string) (ed25519.PrivateKey, error) {
	var pemBytes []byte
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, err
		}
		pemBytes = b
	} else {
		pemBytes = []byte(os.Getenv("NOVA_PLUGIN_SIGN_KEY"))
	}
	if len(bytes.TrimSpace(pemBytes)) == 0 {
		return nil, errors.New("没有私钥：设置环境变量 NOVA_PLUGIN_SIGN_KEY，或用 -key 指定文件")
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("私钥不是 PEM 格式")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("私钥无法解析：%w", err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 ed25519")
	}
	return priv, nil
}

func cmdVerify(args []string) error {
	fsx := flag.NewFlagSet("verify", flag.ExitOnError)
	p := fsx.String("index", "index.json", "仓库清单")
	pubB64 := fsx.String("pub", "", "公钥（base64；不填时由 -key 或环境变量 NOVA_PLUGIN_SIGN_KEY 的私钥求出）")
	keyFile := fsx.String("key", "", "私钥 PEM 文件（只在不填 -pub 时使用）")
	fsx.Parse(args)
	if *pubB64 == "" {
		priv, err := loadPrivateKey(*keyFile)
		if err != nil {
			return fmt.Errorf("需要 -pub，或提供私钥：%w", err)
		}
		*pubB64 = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	}
	pub, err := base64.StdEncoding.DecodeString(*pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("公钥格式不对")
	}
	b, err := os.ReadFile(*p)
	if err != nil {
		return err
	}
	sigB, err := os.ReadFile(*p + ".sig")
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), b, sig) {
		return errors.New("签名验证失败")
	}
	fmt.Println("签名正确")
	return nil
}
