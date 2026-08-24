package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, DefaultConfigName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const miniConfig = `{
  "tools": [
    {
      "name": "sys.echo",
      "summary": "回声",
      "exec": {"program": "/bin/echo", "args": ["{msg}"]},
      "input": {
        "type": "object",
        "properties": {"msg": {"type": "string"}}
      }
    }
  ]
}`

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, miniConfig)
	reg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := reg.Get("sys.echo"); !ok {
		t.Fatalf("names = %v, want sys.echo", reg.Names())
	}
}

func TestLoadFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(miniConfig))
	}))
	defer srv.Close()
	reg, err := Load(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Load(url): %v", err)
	}
	if _, ok := reg.Get("sys.echo"); !ok {
		t.Fatalf("names = %v, want sys.echo", reg.Names())
	}
	// 非 200 报错。
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	if _, err := Load(context.Background(), srv404.URL); err == nil {
		t.Error("404 fetch accepted")
	}
}

func TestLoadDefaultConventions(t *testing.T) {
	// 目录里有 xyz.json：默认查找命中。
	dir := t.TempDir()
	writeConfig(t, dir, miniConfig)
	t.Chdir(dir)
	reg, err := LoadDefault(context.Background())
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if _, ok := reg.Get("sys.echo"); !ok {
		t.Fatalf("names = %v", reg.Names())
	}

	// XYZ_CONFIG 优先级更高。
	dir2 := t.TempDir()
	other := writeConfig(t, dir2, `{"tools":[{"name":"alt.tool","exec":{"program":"/bin/true"}}]}`)
	t.Setenv("XYZ_CONFIG", other)
	reg, err = LoadDefault(context.Background())
	if err != nil {
		t.Fatalf("LoadDefault(env): %v", err)
	}
	if _, ok := reg.Get("alt.tool"); !ok {
		t.Fatalf("names = %v, want alt.tool from XYZ_CONFIG", reg.Names())
	}

	// 无配置文件且无环境变量：报错指出约定。
	t.Chdir(t.TempDir())
	t.Setenv("XYZ_CONFIG", "")
	if _, err := LoadDefault(context.Background()); err == nil {
		t.Error("missing config accepted")
	}
}

func TestParseConfigRejects(t *testing.T) {
	if _, err := ParseConfig([]byte(`{"tools": "nope"}`)); err == nil {
		t.Error("tools as string accepted")
	}
	if _, err := ParseConfig([]byte(`{`)); err == nil {
		t.Error("broken JSON accepted")
	}
}
