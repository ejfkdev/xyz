package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	xyzcli "github.com/ejfkdev/xyz-go/cli"
	"github.com/ejfkdev/xyz-go/httpapi"
	"github.com/ejfkdev/xyz-go/registry"
)

// testSchema 是端到端用例的共享入参 schema。
func testSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host":  map[string]any{"type": "string", "description": "域名"},
			"qtype": map[string]any{"type": "string", "enum": []any{"A", "AAAA"}, "default": "A"},
			"count": map[string]any{"type": "integer", "default": float64(1)},
			"deep":  map[string]any{"type": "boolean"},
			"cfg": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verbose": map[string]any{"type": "boolean"},
				},
			},
		},
		"required": []any{"host"},
	}
}

func TestNewToolThreeFronts(t *testing.T) {
	var last map[string]any
	inv := func(_ context.Context, args map[string]any) (any, error) {
		last = args
		return []string{"done"}, nil
	}
	e, err := NewTool("dns.lookup", "DNS 查询", "解析域名", testSchema(), inv)
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	reg := registry.New()
	if err := reg.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// —— CLI 前端：动态元数据被接受，flag 解析 + 默认注入走同一条管线 ——
	app, err := xyzcli.New(reg)
	if err != nil {
		t.Fatalf("cli.New: %v", err)
	}
	if code := app.Run([]string{"dns", "lookup", "--host", "example.com", "--deep"}); code != 0 {
		t.Fatalf("cli run = %d, want 0", code)
	}
	if last == nil || last["host"] != "example.com" || last["deep"] != true {
		t.Fatalf("cli args = %#v", last)
	}
	if last["qtype"] != "A" || last["count"] != int64(1) {
		t.Errorf("cli defaults missing: qtype=%#v count=%#v", last["qtype"], last["count"])
	}
	// 缺 required：invalid_input 映射为失败退出码。
	if code := app.Run([]string{"dns", "lookup"}); code == 0 {
		t.Error("missing required host accepted by CLI")
	}

	// —— HTTP 前端：POST /tools/dns/lookup + JSON body ——
	h, err := httpapi.Handler(reg)
	if err != nil {
		t.Fatalf("httpapi.Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tools/dns/lookup", strings.NewReader(`{"host":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("http status = %d, body %s", rec.Code, rec.Body.String())
	}
	if last == nil || last["host"] != "x" || last["qtype"] != "A" {
		t.Errorf("http args = %#v", last)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/tools/dns/lookup", strings.NewReader(`{}`))
	h.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("missing required via HTTP accepted: %d", rec.Code)
	}

	// —— MCP 前端吃的 InputSchema 与声明同源 ——
	b, err := json.Marshal(e.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host", "qtype", `"enum":["A","AAAA"]`, `"default":"A"`, "cfg"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("InputSchema missing %q: %s", want, b)
		}
	}
}

func TestNewToolRejects(t *testing.T) {
	inv := func(_ context.Context, _ map[string]any) (any, error) { return nil, nil }
	if _, err := NewTool("", "", "", nil, inv); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := NewTool("a.b", "", "", nil, nil); err == nil {
		t.Error("nil invoker accepted")
	}
	if _, err := NewTool("a.b", "", "", map[string]any{"type": "regex"}, inv); err == nil {
		t.Error("unsupported schema accepted")
	}
	if _, err := NewTool("a.b", "", "", map[string]any{"type": "string"}, inv); err == nil {
		t.Error("non-object root accepted")
	}
}

func TestConfigBuildExec(t *testing.T) {
	cfg := &Config{Tools: []ToolConfig{{
		Name:    "sys.echo",
		Summary: "回声",
		Exec:    &ExecConfig{Program: "/bin/echo", Args: []string{"{msg}"}},
		Input: map[string]any{
			"type":       "object",
			"properties": map[string]any{"msg": map[string]any{"type": "string"}},
		},
	}}}
	reg, err := cfg.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	e, ok := reg.Get("sys.echo")
	if !ok {
		t.Fatal("sys.echo not registered")
	}
	out, err := e.Invoke(context.Background(), map[string]any{"msg": "go"})
	if err != nil || out != "go" {
		t.Fatalf("invoke = %#v, %v", out, err)
	}
	// 配置期错误：两个适配器、坏占位符、无适配器。
	for _, bad := range []*Config{
		{Tools: []ToolConfig{{Name: "x"}}}, // 无适配器
		{Tools: []ToolConfig{{Name: "x", Exec: &ExecConfig{Program: "x"}, MCP: &MCPConfig{Command: "y"}}}},
		{Tools: []ToolConfig{{Name: "x", Exec: &ExecConfig{Program: "x", Args: []string{"{ghost}"}}, Input: testSchema()}}},
	} {
		if _, err := bad.Build(context.Background()); err == nil {
			t.Errorf("bad config accepted: %+v", bad)
		}
	}
}

func TestConfigBuildEmpty(t *testing.T) {
	reg, err := (&Config{}).Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(reg.Names()) != 0 {
		t.Errorf("names = %v, want empty registry", reg.Names())
	}
}

func TestNestedObjectSkippedByCLIOnly(t *testing.T) {
	inv := func(_ context.Context, _ map[string]any) (any, error) { return nil, nil }
	e, err := NewTool("a.b", "", "", testSchema(), inv)
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	found := false
	for _, f := range e.Root.Fields {
		if f.JSONName == "cfg" {
			found = true
			if !f.CLI.Skip {
				t.Error("nested object property must be CLI-skipped")
			}
			if f.Skip {
				t.Error("nested object property must stay visible to MCP/HTTP (skip=false)")
			}
		}
	}
	if !found {
		t.Fatal("cfg property missing from field tree")
	}
	if e.InputSchema.Properties["cfg"] == nil {
		t.Error("InputSchema must keep the nested object for the MCP frontend")
	}
}
