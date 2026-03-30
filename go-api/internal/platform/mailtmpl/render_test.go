package mailtmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderUsesBladeTemplatePlaceholders(t *testing.T) {
	root := t.TempDir()
	templateDir := filepath.Join(root, "resources", "views", "mail", "forest-v2")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	raw := `<html><body><h1>{{$name}}</h1><div>{{$code}}</div><p>{!! nl2br(e($content)) !!}</p><a href="{{$url}}">back</a></body></html>`
	if err := os.WriteFile(filepath.Join(templateDir, "verify.blade.php"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	body, htmlBody, err := Render(root, "forest-v2", "verify", map[string]string{
		"name":    "Forest",
		"code":    "123456",
		"url":     "https://forest.example.com",
		"content": "line1\nline2",
	})
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	if !htmlBody {
		t.Fatal("expected html body")
	}
	if !strings.Contains(body, "<h1>Forest</h1>") {
		t.Fatalf("expected name replacement, got %q", body)
	}
	if !strings.Contains(body, "123456") {
		t.Fatalf("expected code replacement, got %q", body)
	}
	if !strings.Contains(body, "line1<br>line2") {
		t.Fatalf("expected nl2br content rendering, got %q", body)
	}
}

func TestRenderKeepsRawHTMLForUnescapedNl2br(t *testing.T) {
	root := t.TempDir()
	templateDir := filepath.Join(root, "resources", "views", "mail", "default")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatalf("mkdir template dir: %v", err)
	}
	raw := `<div>{!! nl2br($content) !!}</div>`
	if err := os.WriteFile(filepath.Join(templateDir, "notify.blade.php"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	body, _, err := Render(root, "default", "notify", map[string]string{
		"content": "<b>hi</b>\n<a href=\"https://forest.example.com\">jump</a>",
	})
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	if !strings.Contains(body, "<b>hi</b><br><a href=\"https://forest.example.com\">jump</a>") {
		t.Fatalf("expected raw html to remain unescaped, got %q", body)
	}
}
