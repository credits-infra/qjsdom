package main

import (
	"strings"
	"testing"

	"qjsdom/internal/renderer"
)

func TestParseDefaultMode(t *testing.T) {
	cases := []struct {
		raw      string
		want     renderer.Mode
		wantWarn bool
	}{
		{"", renderer.ModeAll, false},
		{"all", renderer.ModeAll, false},
		{"ALL", renderer.ModeAll, false},
		{"static", renderer.ModeStatic, false},
		{" STATIC ", renderer.ModeStatic, false},
		{"article", renderer.ModeAll, true},
		{"links", renderer.ModeAll, true},
		{"js", renderer.ModeAll, true},
		{"nonsense", renderer.ModeAll, true},
	}
	for _, c := range cases {
		got, warning := parseDefaultMode(c.raw)
		if got != c.want {
			t.Errorf("parseDefaultMode(%q) = %q,想要 %q", c.raw, got, c.want)
		}
		if (warning != "") != c.wantWarn {
			t.Errorf("parseDefaultMode(%q) warning=%q,想要有警告=%v", c.raw, warning, c.wantWarn)
		}
		if warning != "" && !strings.Contains(warning, "all 或 static") {
			t.Errorf("警告没有说明允许值: %q", warning)
		}
	}
}

func TestLoadConfigReadsDefaultMode(t *testing.T) {
	t.Setenv("QJSDOM_DEFAULT_MODE", "static")
	cfg := loadConfig()
	if cfg.defaultMode != renderer.ModeStatic {
		t.Errorf("defaultMode = %q,想要 static", cfg.defaultMode)
	}
	if cfg.defaultModeWarning != "" {
		t.Errorf("合法值不该有警告: %q", cfg.defaultModeWarning)
	}

	t.Setenv("QJSDOM_DEFAULT_MODE", "article")
	cfg = loadConfig()
	if cfg.defaultMode != renderer.ModeAll {
		t.Errorf("非法值应当回落到 all,实际 %q", cfg.defaultMode)
	}
	if !strings.Contains(cfg.defaultModeWarning, "article/links") {
		t.Errorf("非法值应当提示 article/links 不能做默认: %q", cfg.defaultModeWarning)
	}
}

func TestEffectiveMode(t *testing.T) {
	cases := []struct {
		requested string
		def       renderer.Mode
		want      renderer.Mode
	}{
		{"", renderer.ModeAll, renderer.ModeAll},
		{"", renderer.ModeStatic, renderer.ModeStatic},
		{"", "", renderer.ModeAll}, // 零值 config 也回落到 all
		{"static", renderer.ModeAll, renderer.ModeStatic},
		{"all", renderer.ModeStatic, renderer.ModeAll},
		{"article", renderer.ModeStatic, renderer.ModeArticle},
		{"links", renderer.ModeAll, renderer.ModeLinks},
		{" STATIC ", renderer.ModeAll, renderer.ModeStatic},
		{"nonsense", renderer.ModeStatic, renderer.ModeStatic},
		{"nonsense", renderer.ModeAll, renderer.ModeAll},
	}
	for _, c := range cases {
		if got := effectiveMode(c.requested, c.def); got != c.want {
			t.Errorf("effectiveMode(%q, %q) = %q,想要 %q", c.requested, c.def, got, c.want)
		}
	}
}

func TestToolDescriptionReflectsDefault(t *testing.T) {
	all := buildToolDescription(renderer.ModeAll)
	if !strings.Contains(all, "By default this server uses mode=all") {
		t.Errorf("all 默认的描述没有说清默认值:\n%s", all)
	}
	if !strings.Contains(all, "Use mode=links for search results") {
		t.Errorf("all 默认的描述丢了 links 指引:\n%s", all)
	}
	if strings.Contains(all, "This server defaults to mode=static") {
		t.Errorf("all 默认的描述不该声称默认 static:\n%s", all)
	}

	static := buildToolDescription(renderer.ModeStatic)
	if !strings.Contains(static, "This server defaults to mode=static") {
		t.Errorf("static 默认的描述没有说清默认值:\n%s", static)
	}
	if !strings.Contains(static, "does not return images") {
		t.Errorf("static 默认的描述没有说清没有图片:\n%s", static)
	}
	if !strings.Contains(static, "including search results") {
		t.Errorf("static 默认的描述没有给出搜索页的做法:\n%s", static)
	}
	if !strings.Contains(static, "both start the script runtime") {
		t.Errorf("static 默认的描述没有说明 article/links 会启动运行时:\n%s", static)
	}
	if !strings.Contains(static, "With mode=all or mode=article, page images") {
		t.Errorf("static 默认的描述没有说明怎么拿图片:\n%s", static)
	}
	if strings.Contains(static, "By default this server uses mode=all") {
		t.Errorf("static 默认的描述不该声称默认 all:\n%s", static)
	}
	if strings.Contains(static, "By default, page scripts") {
		t.Errorf("static 默认的描述不该把脚本联网写成默认行为:\n%s", static)
	}

	for _, desc := range []string{all, static} {
		if !strings.Contains(desc, "An explicit mode overrides the server default.") {
			t.Errorf("描述没有说明显式 mode 覆盖默认值:\n%s", desc)
		}
	}
}
