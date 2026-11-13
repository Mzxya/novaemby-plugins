package main

import (
	"bytes"
	"encoding/json"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Runs under the unchanged official Go-only CI. It checks asset contracts and
// authorization; full JavaScript behavior tests remain optional for reviewers.
func TestFrontendResourceContracts(t *testing.T) {
	html, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{"detailHistory": true}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllSubmatch(html, -1) {
		ids[string(match[1])] = true
	}
	for _, match := range regexp.MustCompile(`\$\("([^"]+)"\)`).FindAllSubmatch(app, -1) {
		if !ids[string(match[1])] {
			t.Fatalf("controller refers to missing element #%s", match[1])
		}
	}
	if bytes.Contains(html, []byte("https://")) || bytes.Contains(html, []byte("http://")) {
		t.Fatal("page must not load external resources")
	}
	if !bytes.Contains(app, []byte(`from "./core.mjs"`)) {
		t.Fatal("missing frontend module")
	}
	core, err := assets.ReadFile("assets/core.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`credentials:"same-origin"`, `X-Nova-CSRF`} {
		if !bytes.Contains(core, []byte(required)) {
			t.Fatal("missing same-origin request behavior", required)
		}
	}
	for _, name := range []string{"theme.js", "styles.css"} {
		if _, err := assets.ReadFile("assets/" + name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGoSourcesAreFormatted(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !bytes.Equal(raw, formatted) {
			t.Errorf("%s must be formatted using gofmt", path)
		}
	}
}

func TestSubmissionMetadata(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version, Author, Homepage string
		Config                    []struct{ Key, Description string }
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest.Version) || manifest.Author != "jenley" || !strings.HasPrefix(manifest.Homepage, "https://") {
		t.Fatal("invalid initial contribution metadata")
	}
	for _, c := range manifest.Config {
		if c.Key == "verboseEventLog" && (!strings.Contains(c.Description, "用户名") || !strings.Contains(c.Description, "设备")) {
			t.Fatal("raw-event logging must disclose user/device information")
		}
	}
}
