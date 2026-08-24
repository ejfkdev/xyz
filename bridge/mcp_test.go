package bridge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	xyzmcp "github.com/ejfkdev/xyz-go/mcp"
	"github.com/ejfkdev/xyz-go/registry"
	"github.com/ejfkdev/xyz-go/spec"
)

// TestMCPProxyHelper 是子进程形态宿主：被测进程用 -test.run=TestMCPProxyHelper
// + 环境变量把自己重跑成一台 stdio MCP server（服务端用 xyz-go 官方前端），
// 供 TestMCPProxyRoundTrip 以真实子进程方式代理。
func TestMCPProxyHelper(t *testing.T) {
	if os.Getenv("XYZ_BRIDGE_MCP_HELPER") == "1" {
		for _, a := range os.Args {
			if strings.Contains(a, "TestMCPProxyHelper") {
				runMCPHelper()
			}
		}
	}
	// 常规测试运行里直接返回（无操作）。
}

func runMCPHelper() {
	type echoArgs struct {
		Msg string `json:"msg" required:"true"`
	}
	reg := registry.New()
	if _, err := spec.Define("echo.msg", func(_ context.Context, in *echoArgs) (string, error) {
		return "pong:" + in.Msg, nil
	}).Register(reg); err != nil {
		panic(err)
	}
	srv, err := xyzmcp.Server(reg, xyzmcp.Options{})
	if err != nil {
		panic(err)
	}
	// stdio 全链路：桥接库的 StartMCPProxy 用管道充当我们的 stdin/stdout。
	if err := srv.Run(context.Background(), &sdkmcp.IOTransport{Reader: os.Stdin, Writer: os.Stdout}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPProxyRoundTrip(t *testing.T) {
	if os.Getenv("XYZ_BRIDGE_MCP_HELPER") == "1" {
		t.Skip("helper process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	t.Setenv("XYZ_BRIDGE_MCP_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proxy, err := StartMCPProxy(ctx, MCPConfig{
		Command: exe,
		Args:    []string{"-test.run=TestMCPProxyHelper", "-test.v=false"},
	}, "demo.")
	if err != nil {
		t.Fatalf("StartMCPProxy: %v", err)
	}
	t.Cleanup(proxy.Close)

	display := "demo.echo.msg"
	if _, ok := proxy.tools[display]; !ok {
		t.Fatalf("tools = %v, want %s listed", proxy.tools, display)
	}
	out, err := proxy.InvokeTool(display)(ctx, map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("proxy call: %v", err)
	}
	if out != "pong:hi" {
		t.Errorf("out = %#v, want pong:hi", out)
	}
	// 远端参数校验失败以 isError 返回 → 分类 internal。
	if _, err := proxy.InvokeTool(display)(ctx, map[string]any{}); err == nil {
		t.Error("remote validation error swallowed")
	}

	// Build 级联：把代理直接注册成三通道命令；MCPPrefix 只改 MCP 工具名。
	cfg := &Config{
		MCPPrefix: "pfx.",
		Tools: []ToolConfig{{
			Name: "repomix",
			MCP: &MCPConfig{
				Command: exe,
				Args:    []string{"-test.run=TestMCPProxyHelper", "-test.v=false"},
			},
		}},
	}
	reg, err := cfg.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	e, ok := reg.Get("repomix.echo.msg")
	if !ok {
		t.Fatalf("repomix.echo.msg not registered; names: %v", reg.Names())
	}
	if e.MCP.Name != "pfx.repomix.echo.msg" {
		t.Errorf("proxied MCP name = %q, want pfx.repomix.echo.msg", e.MCP.Name)
	}
	if e.HTTP.Path != "/tools/repomix/echo/msg" {
		t.Errorf("proxied HTTP path = %q, want /tools/repomix/echo/msg", e.HTTP.Path)
	}
	got, err := e.Invoke(ctx, map[string]any{"msg": "full"})
	if err != nil || got != "pong:full" {
		t.Fatalf("full build invoke = %#v, %v", got, err)
	}
	// 退出清理已由 t.Cleanup 覆盖 proxy；Build 起的进程跟随进程生命周期。
}

func TestImportToolsList(t *testing.T) {
	// tools/list 响应形态
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
		{"name":"a.b","description":"第一行\n详情","inputSchema":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}},
		{"name":"c.d","description":"另一个","inputSchema":{"type":"object"}}
	]}}`)
	tools, err := ImportToolsList(raw)
	if err != nil {
		t.Fatalf("ImportToolsList: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "a.b" || tools[0].Input["type"] != "object" {
		t.Errorf("tools = %+v", tools)
	}
	if firstLine(tools[0].Description) != "第一行" {
		t.Errorf("firstLine = %q", firstLine(tools[0].Description))
	}
	// 裸数组形态
	_, err = ImportToolsList([]byte(`[{"name":"x","inputSchema":{"type":"object"}}]`))
	if err != nil {
		t.Errorf("bare array rejected: %v", err)
	}
	if _, err := ImportToolsList([]byte(`{"result":{}}`)); err == nil {
		t.Error("payload without tools accepted")
	}
}
