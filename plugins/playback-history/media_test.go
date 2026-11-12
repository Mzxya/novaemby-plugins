package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlySameOriginPosterTemplates(t *testing.T) {
	for _, good := range []string{"", "/confirmed/images/{id}?width=200"} {
		if err := validatePosterTemplate(good); err != nil {
			t.Fatal(good, err)
		}
	}
	for _, bad := range []string{"https://external/images/{id}", "//external/{id}", "/\\external/{id}", "/image/no-placeholder", "/image/{id}?api_key=secret"} {
		if validatePosterTemplate(bad) == nil {
			t.Fatal("不应接受", bad)
		}
	}
}
func TestArtworkComesFromSidecarAndHasImageContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	paths := sidecarPaths(path, false)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(dir, "poster.png")
	if err := os.WriteFile(poster, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	body, mime, err := readArtwork(paths)
	if err != nil || mime != "image/png" || !bytes.Equal(body, data.Bytes()) {
		t.Fatal("没有读取到实际封面", err, mime)
	}
	os.Remove(poster)
	if err = os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("not image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readArtwork(paths); err == nil {
		t.Fatal("扩展名伪装的非图片被返回")
	}
	if sidecarPaths("../../secret", false) != nil {
		t.Fatal("接受相对媒体路径")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			t.Fatal("封面越出官方媒体目录")
		}
	}
}
