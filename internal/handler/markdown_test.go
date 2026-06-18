package handler

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_Basic(t *testing.T) {
	html, err := renderMarkdown("# Hello\n\nworld")
	if err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	if !strings.Contains(html, "<h1") {
		t.Errorf("expected <h1, got %s", html)
	}
	if !strings.Contains(html, "Hello</h1>") {
		t.Errorf("expected h1 content 'Hello', got %s", html)
	}
	if !strings.Contains(html, "<p>world</p>") {
		t.Errorf("expected <p>world</p>, got %s", html)
	}
}

func TestRenderMarkdown_LinkHasTargetBlank(t *testing.T) {
	html, _ := renderMarkdown("[cfgsync](https://example.com)")
	if !strings.Contains(html, `<a target="_blank" rel="noopener" href="https://example.com">cfgsync</a>`) {
		t.Errorf("link missing target/rel: %s", html)
	}
}

func TestRenderMarkdown_RawScriptTagOmitted(t *testing.T) {
	src := `<script>alert("xss")</script>`
	html, _ := renderMarkdown(src)
	// goldmark omits raw HTML — no live <script> tag, and a visible
	// "<!-- raw HTML omitted -->" marker so it's clear in -v output
	// that something was stripped.
	if strings.Contains(html, "<script>") {
		t.Errorf("raw <script> tag survived rendering: %s", html)
	}
	if !strings.Contains(html, "<!-- raw HTML omitted -->") {
		t.Errorf("expected raw HTML omitted marker, got %s", html)
	}
}

func TestRenderMarkdown_RawAnchorOmitted(t *testing.T) {
	// Raw <a href="evil"> in source must NOT become a live link. The
	// post-process ReplaceAll only targets goldmark's own <a href=,
	// which is generated from "[text](url)" syntax — never from raw HTML.
	src := `<a href="javascript:evil">click</a>`
	html, _ := renderMarkdown(src)
	if strings.Contains(html, `<a target="_blank" rel="noopener" href="javascript:evil">`) {
		t.Errorf("raw anchor got target=_blank (would be live): %s", html)
	}
	if !strings.Contains(html, "<!-- raw HTML omitted -->") {
		t.Errorf("expected raw HTML omitted marker, got %s", html)
	}
}

func TestRenderMarkdown_CodeBlock(t *testing.T) {
	html, _ := renderMarkdown("```go\nfmt.Println(\"hi\")\n```")
	if !strings.Contains(html, "<pre>") {
		t.Errorf("expected <pre> in code block, got %s", html)
	}
	// goldmark emits <code class="language-go">, not bare <code>.
	if !strings.Contains(html, "<code") {
		t.Errorf("expected <code in code block, got %s", html)
	}
	if !strings.Contains(html, "Println") {
		t.Errorf("code content missing, got %s", html)
	}
}

func TestRenderMarkdown_List(t *testing.T) {
	html, _ := renderMarkdown("- one\n- two\n- three\n")
	if !strings.Contains(html, "<ul>") {
		t.Errorf("expected <ul>, got %s", html)
	}
	if strings.Count(html, "<li>") != 3 {
		t.Errorf("expected 3 list items, got %s", html)
	}
}
