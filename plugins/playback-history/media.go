package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mzxya/novaemby-plugins/sdk"
)

func validatePosterTemplate(template string) error {
	if template == "" {
		return nil
	}
	if !strings.Contains(template, "{id}") {
		return errors.New("封面地址必须包含 {id}")
	}
	u, err := url.Parse(strings.ReplaceAll(template, "{id}", "1"))
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(template, "\\\r\n") {
		return errors.New("封面地址必须是同源绝对路径，不能包含域名")
	}
	for k := range u.Query() {
		lower := strings.ToLower(k)
		if lower == "api_key" || lower == "token" || lower == "access_token" {
			return errors.New("封面地址不能包含 Token 或 API 密钥")
		}
	}
	return nil
}
func requestContext(r *sdk.Request) context.Context {
	if r.Context != nil {
		return r.Context
	}
	return context.Background()
}

// Only sibling artwork of paths returned by official host.item is read.
// Neither request parameters nor records can supply a filesystem path.
func sidecarPaths(mediaPath string, directory bool) []string {
	if !filepath.IsAbs(mediaPath) || filepath.Clean(mediaPath) == string(filepath.Separator) {
		return nil
	}
	dir := mediaPath
	if !directory {
		dir = filepath.Dir(mediaPath)
	}
	names := []string{"poster.jpg", "poster.png", "poster.webp", "folder.jpg", "cover.jpg"}
	if !directory {
		base := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
		out := []string{base + "-poster.jpg", base + "-poster.png", base + ".jpg"}
		for _, n := range names {
			out = append(out, filepath.Join(dir, n))
		}
		return out
	}
	out := []string{}
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}
func readArtwork(paths []string) ([]byte, string, error) {
	for _, path := range paths {
		stat, err := os.Lstat(path)
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 8<<20 {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		file.Close()
		if err != nil || len(data) > 8<<20 {
			continue
		}
		mime := http.DetectContentType(data)
		if mime == "image/jpeg" || mime == "image/png" || mime == "image/webp" {
			return data, mime, nil
		}
	}
	return nil, "", os.ErrNotExist
}
func (a *application) poster(h *sdk.Host, r *sdk.Request) *sdk.Response {
	record, ok := a.store.Get(r.Params["id"])
	if !ok || record.ItemID == "" {
		return sdk.Text(404, "No artwork.")
	}
	ctx, cancel := context.WithTimeout(requestContext(r), 3*time.Second)
	defer cancel()
	ids := []string{record.ItemID}
	if record.SeriesID != "" && record.SeriesID != record.ItemID {
		ids = append([]string{record.SeriesID}, ids...)
	}
	for _, id := range ids {
		item, err := h.Item(ctx, id)
		if err != nil {
			continue
		}
		stat, err := os.Stat(item.Path)
		if err != nil {
			continue
		}
		paths := sidecarPaths(item.Path, stat.IsDir())
		data, mime, err := readArtwork(paths)
		if err != nil {
			continue
		}
		resp := sdk.Bytes(200, mime, data)
		resp.Header.Set("Cache-Control", "private, max-age=300")
		resp.Header.Set("X-Content-Type-Options", "nosniff")
		return resp
	}
	return sdk.Text(404, "No accessible sidecar artwork. Configure a verified same-origin image path if available.")
}
func (a *application) media(h *sdk.Host, r *sdk.Request) *sdk.Response {
	record, ok := a.store.Get(r.Params["id"])
	if !ok {
		return fail(404, "记录不存在")
	}
	result := map[string]any{"record": record.view(a.store.now()), "available": false}
	if record.ItemID != "" {
		ctx, cancel := context.WithTimeout(requestContext(r), 3*time.Second)
		defer cancel()
		item, err := h.Item(ctx, record.ItemID)
		if err == nil && item != nil && (item.ID != "" || item.Name != "") {
			// Intentionally omit the host filesystem path and provider credentials.
			result["available"] = true
			result["item"] = map[string]any{"id": item.ID, "name": item.Name, "originalTitle": item.OriginalTitle, "type": item.Type, "year": item.Year, "seriesName": item.SeriesName, "seasonNumber": item.SeasonNumber, "episodeNumber": item.EpisodeNumber, "runtimeTicks": item.RuntimeTicks, "providerIds": item.ProviderIDs}
		}
	}
	return sdk.JSON(200, result)
}
