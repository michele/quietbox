package config

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The example configs and the complete TOML blocks in the docs are what a new
// user copies first. They must parse with the real loader, so a renamed key or a
// typo cannot ship.

func TestExampleConfigsParse(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"examples/fastmail.toml",
		"examples/gmail.toml",
		"examples/generic-imap.toml",
		"deploy/quietbox.example.toml",
	}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, f))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := Parse(data, env(map[string]string{PasswordEnv: "secret"}))
			if err != nil {
				t.Fatalf("%s does not parse: %v", f, err)
			}
			if cfg.IMAP.Host == "" || cfg.IMAP.Username == "" {
				t.Errorf("%s: host and username must be set (placeholders are fine)", f)
			}
			if !cfg.Blackhole.SweepEnabled() {
				t.Errorf("%s: the default retention sweep should be on", f)
			}
		})
	}
}

var tomlBlock = regexp.MustCompile("(?s)```toml\n(.*?)```")

// A block is a complete config when it has an [imap] section and folders; the
// partial snippets that show one section are skipped.
func TestDocsTOMLBlocksParse(t *testing.T) {
	root := filepath.Join("..", "..")
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs = append(docs, filepath.Join(root, "README.md"))
	complete := 0
	for _, doc := range docs {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range tomlBlock.FindAllStringSubmatch(string(data), -1) {
			block := m[1]
			if !strings.Contains(block, "[imap]") || !strings.Contains(block, "[[folder]]") {
				continue
			}
			complete++
			if _, err := Parse([]byte(block), env(map[string]string{PasswordEnv: "secret"})); err != nil {
				t.Errorf("%s: TOML block %d does not parse: %v", filepath.Base(doc), i+1, err)
			}
		}
	}
	if complete == 0 {
		t.Error("found no complete TOML block in the docs: is the test looking in the right place?")
	}
}

var htmlTOMLBlock = regexp.MustCompile(`(?s)<pre data-lang="toml"><code>(.*?)</code></pre>`)

// The website's guide carries configs people copy too. A <pre data-lang="toml">
// block is a complete config and must parse.
func TestSiteTOMLBlocksParse(t *testing.T) {
	root := filepath.Join("..", "..")
	pages, err := filepath.Glob(filepath.Join(root, "site", "*", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	pages = append(pages, filepath.Join(root, "site", "index.html"))
	complete := 0
	for _, page := range pages {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range htmlTOMLBlock.FindAllStringSubmatch(string(data), -1) {
			complete++
			block := html.UnescapeString(m[1])
			if _, err := Parse([]byte(block), env(map[string]string{PasswordEnv: "secret"})); err != nil {
				t.Errorf("%s: TOML block %d does not parse: %v", page, i+1, err)
			}
		}
	}
	if complete == 0 {
		t.Error("found no TOML block on the site: is the test looking in the right place?")
	}
}
