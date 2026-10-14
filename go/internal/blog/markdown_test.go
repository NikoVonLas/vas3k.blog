package blog

import (
	"strings"
	"testing"
	"time"
)

func TestMarkdownBlocksAndSafety(t *testing.T) {
	m := NewMarkdown()
	cases := []struct {
		name, source string
		want, absent []string
	}{
		{"containers", "[[[wide.#intro\nHello **world**\n]]]\n\n{{{\n![Caption](https://example.org/a.jpg)\n}}}\n\n% Quote", []string{`id="intro"`, `<strong>world</strong>`, `block=10`, `block-media`, `<figure>`, `block-cite`}, nil},
		{"code is literal", "```text\n[[[\nnot a block\n]]]\n[? hidden ?]\n```", []string{"not a block", "[? hidden ?]"}, []string{`class="block-text`, `class="block-spoiler`}},
		{"inline code", "`[? literal ?]` [? secret ?]", []string{"<code>[? literal ?]</code>", `class="block-spoiler"`}, nil},
		{"unclosed", "[[[\nKeep this text", []string{"Keep this text"}, nil},
		{"unicode headings", "## Начало\n\n% quote\n\n## Начало", []string{`id="начало"`, `id="начало-2"`}, nil},
		{"fence inside container", "[[[\n```\n]]]\n```\ntext\n]]]", []string{"<code>", "text", "block=10"}, nil},
		{"unsafe", "<script>alert(1)</script><img src=x onerror=alert(1)>\n[x](javascript:alert(1))", nil, []string{"<script", "onerror=", "javascript:"}},
		{"video", "![Film](https://example.org/a.mp4)", []string{"<video", "controls"}, nil},
		{"youtube", "![Film](https://youtu.be/dQw4w9WgXcQ)", []string{"youtube-nocookie.com/embed/dQw4w9WgXcQ"}, nil},
		{"blocked frame", "<iframe src=\"https://evil.example\"></iframe>", nil, []string{"evil.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(m.Render(tc.source, false))
			for _, s := range tc.want {
				if !strings.Contains(out, s) {
					t.Errorf("missing %q in %s", s, out)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(out, s) {
					t.Errorf("unexpected %q in %s", s, out)
				}
			}
		})
	}
	out := string(m.Comment(`<img src=x onerror=alert(1)><script>alert(1)</script>`))
	if strings.Contains(out, "<script") || strings.Contains(out, "<img") {
		t.Fatalf("unsafe comment: %s", out)
	}
}
func TestPostVisibility(t *testing.T) {
	now := time.Now()
	public := Post{Visible: true, PublishedAt: now.Add(-time.Hour)}
	if !public.Readable(nil) {
		t.Fatal("public post inaccessible")
	}
	public.MembersOnly = true
	if public.Readable(nil) || !public.Readable(&User{}) {
		t.Fatal("membership boundary")
	}
	public.PublishedAt = now.Add(time.Hour)
	if public.Readable(&User{}) || !public.Readable(&User{Admin: true}) {
		t.Fatal("scheduled post boundary")
	}
	public.Visible = false
	if public.Readable(nil) {
		t.Fatal("draft exposed")
	}
}
