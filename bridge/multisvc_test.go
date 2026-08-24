package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	xyzcli "github.com/ejfkdev/xyz-go/cli"
	"github.com/ejfkdev/xyz-go/httpapi"
	"github.com/ejfkdev/xyz-go/registry"
)

// TestHTTPPrefixAndTopLevelName 覆盖"多个 CLI 平铺成一个服务"的两个形态点：
// 不带点分的顶层命令名（CLI 单段 dispatch）与可配的 HTTP 路由前缀。
func TestHTTPPrefixAndTopLevelName(t *testing.T) {
	var last map[string]any
	inv := func(_ context.Context, args map[string]any) (any, error) {
		last = args
		return "ok", nil
	}
	e, err := NewToolWith("ls", "列目录", "", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "default": "."},
		},
	}, inv, ToolOpts{HTTPPrefix: "/api"})
	if err != nil {
		t.Fatalf("NewToolWith: %v", err)
	}
	if e.HTTP.Path != "/api/ls" {
		t.Errorf("HTTP.Path = %q, want /api/ls", e.HTTP.Path)
	}
	reg := registry.New()
	if err := reg.Add(e); err != nil {
		t.Fatal(err)
	}

	// HTTP：路由就挂在 /api/ls。
	h, err := httpapi.Handler(reg)
	if err != nil {
		t.Fatalf("httpapi.Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ls", strings.NewReader(`{"path":"/tmp"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ls = %d, body %s", rec.Code, rec.Body.String())
	}
	if last == nil || last["path"] != "/tmp" {
		t.Errorf("http args = %#v", last)
	}

	// CLI：顶层名单段即命令（xxx ls --path /tmp）。
	app, err := xyzcli.New(reg)
	if err != nil {
		t.Fatalf("cli.New: %v", err)
	}
	if code := app.Run([]string{"ls", "--path", "/tmp"}); code != 0 {
		t.Fatalf("cli run = %d, want 0", code)
	}
	if last == nil || last["path"] != "/tmp" {
		t.Errorf("cli args = %#v", last)
	}

	// 默认前缀回归：不传 opts 仍是 /tools/…。
	plain, err := NewTool("dns.lookup", "", "", nil, inv)
	if err != nil {
		t.Fatal(err)
	}
	if plain.HTTP.Path != "/tools/dns/lookup" {
		t.Errorf("default path = %q, want /tools/dns/lookup", plain.HTTP.Path)
	}
}

// TestLoadManyMergesSources：多个配置文件（每个装一条 CLI）合并成同一个
// 注册表——同名冲突在启动期报错。
func TestLoadManyMergesSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ls := write("ls.json", `{"tools":[{"name":"ls","exec":{"program":"/bin/true"}}]}`)
	whoami := write("whoami.json", `{"tools":[{"name":"whoami","exec":{"program":"/bin/true"}}]}`)
	dup := write("dup.json", `{"tools":[{"name":"ls","exec":{"program":"/bin/false"}}]}`)

	reg, err := LoadMany(ctx, []string{ls, whoami})
	if err != nil {
		t.Fatalf("LoadMany: %v", err)
	}
	if names := reg.Names(); len(names) != 2 {
		t.Fatalf("names = %v, want [ls whoami]", names)
	}
	// 逗号字符串形态是同一个入口。
	comma, err := Load(ctx, ls+","+whoami)
	if err != nil {
		t.Fatalf("Load(comma): %v", err)
	}
	if len(comma.Names()) != 2 {
		t.Fatalf("comma names = %v", comma.Names())
	}
	// 跨源同名：启动期拒绝。
	if _, err := LoadMany(ctx, []string{ls, dup}); err == nil {
		t.Error("duplicate tool name across sources accepted")
	}
	// XYZ_CONFIG 多源与透视空格。
	t.Setenv("XYZ_CONFIG", " "+ls+" , "+whoami+" ")
	reg, err = LoadDefault(ctx)
	if err != nil {
		t.Fatalf("LoadDefault(env multi): %v", err)
	}
	if len(reg.Names()) != 2 {
		t.Fatalf("env names = %v", reg.Names())
	}
}

// TestMultisvcExamples 冒烟 examples/multisvc 的两种组织形态。
func TestMultisvcExamples(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("multisvc 示例按 macOS 命令路径书写")
	}
	ctx := context.Background()
	base := filepath.Join("..", "examples", "multisvc")

	// 形态一：三个文件合并成一台服务。
	reg, err := LoadMany(ctx, []string{
		filepath.Join(base, "ls.json"),
		filepath.Join(base, "whoami.json"),
		filepath.Join(base, "du.json"),
	})
	if err != nil {
		t.Fatalf("LoadMany: %v", err)
	}
	for _, name := range []string{"ls", "whoami", "du"} {
		e, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s missing; names: %v", name, reg.Names())
		}
		out, err := e.Invoke(ctx, map[string]any{"path": "."})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.TrimSpace(out.(string)) == "" {
			t.Errorf("%s: empty output", name)
		}
	}

	// 形态二：单文件多工具 + http_prefix /api。
	creg, err := Load(ctx, filepath.Join(base, "combined.json"))
	if err != nil {
		t.Fatalf("Load(combined): %v", err)
	}
	if names := creg.Names(); len(names) != 3 {
		t.Fatalf("combined names = %v, want 3 tools", names)
	}
	e, _ := creg.Get("ls")
	if e.HTTP.Path != "/api/ls" {
		t.Errorf("combined ls path = %q, want /api/ls", e.HTTP.Path)
	}
	h, err := httpapi.Handler(creg)
	if err != nil {
		t.Fatalf("httpapi.Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/whoami", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) == "" {
		t.Fatalf("POST /api/whoami = %d, body %q", rec.Code, rec.Body.String())
	}
}

// TestChannelPrefixesIndependent 覆盖双前缀需求：http 与 mcp 各自的前缀
// 独立设置、互不影响，CLI 命名始终是点分层级的那份。
func TestChannelPrefixesIndependent(t *testing.T) {
	ctx := context.Background()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "default": "."},
		},
	}
	cfg := &Config{
		HTTPPrefix: "/api",
		MCPPrefix:  "systools.",
		Tools: []ToolConfig{{
			Name:  "ls",
			Exec:  &ExecConfig{Program: "/usr/bin/true"},
			Input: schema,
		}},
	}
	reg, err := cfg.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	e, ok := reg.Get("ls")
	if !ok {
		t.Fatalf("names = %v", reg.Names())
	}
	if e.HTTP.Path != "/api/ls" {
		t.Errorf("HTTP.Path = %q, want /api/ls", e.HTTP.Path)
	}
	if e.MCP.Name != "systools.ls" {
		t.Errorf("MCP.Name = %q, want systools.ls", e.MCP.Name)
	}
	if e.Name != "ls" {
		t.Errorf("entry name = %q, want ls (CLI untouched)", e.Name)
	}
	// CLI 依然顶层 dispatch：ls，不带任何前缀。
	app, err := xyzcli.New(reg)
	if err != nil {
		t.Fatalf("cli.New: %v", err)
	}
	if code := app.Run([]string{"ls", "--path", "/tmp"}); code != 0 {
		t.Fatalf("cli run = %d, want 0", code)
	}

	// 逐工具 MCPName 胜过配置级 MCPPrefix。
	cfg2 := &Config{
		MCPPrefix: "generic.",
		Tools: []ToolConfig{{
			Name:    "x",
			Exec:    &ExecConfig{Program: "/usr/bin/true"},
			Input:   schema,
			MCPName: "pinned.x",
		}},
	}
	reg2, err := cfg2.Build(ctx)
	if err != nil {
		t.Fatalf("Build2: %v", err)
	}
	if got := reg2.All()[0].MCP.Name; got != "pinned.x" {
		t.Errorf("pinned MCP name = %q, want pinned.x", got)
	}

	// 只设 MCP 前缀时 HTTP 保持默认 /tools。
	cfg3 := &Config{
		MCPPrefix: "m.",
		Tools: []ToolConfig{{
			Name:  "x",
			Exec:  &ExecConfig{Program: "/usr/bin/true"},
			Input: schema,
		}},
	}
	reg3, err := cfg3.Build(ctx)
	if err != nil {
		t.Fatalf("Build3: %v", err)
	}
	e3 := reg3.All()[0]
	if e3.MCP.Name != "m.x" || e3.HTTP.Path != "/tools/x" {
		t.Errorf("mcp-only prefix: MCP=%q HTTP=%q, want m.x / /tools/x", e3.MCP.Name, e3.HTTP.Path)
	}
}
