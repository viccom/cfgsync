package handler

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
)

// renderMarkdown converts src to safe HTML via goldmark.
//
// Safety: goldmark's default renderer OMITS raw HTML — user-supplied
// `<script>`, `<a>`, etc. tags are replaced with `<!-- raw HTML omitted -->`
// comments rather than emitted as live HTML. This is the load-bearing XSS
// defense — do not call goldmark.WithUnsafe() without a separate sanitizer.
//
// Links get target="_blank" rel="noopener" via a post-process ReplaceAll.
// This is safe because goldmark's omitted output means the only literal
// "<a href=" substring in the rendered HTML comes from goldmark's own
// link renderer (generated from "[text](url)" syntax), never from the
// source markdown's raw HTML.
func renderMarkdown(src string) (string, error) {
	var buf bytes.Buffer
	if err := goldmark.New().Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	html := buf.String()
	html = strings.ReplaceAll(html, "<a href=", `<a target="_blank" rel="noopener" href=`)
	return html, nil
}
