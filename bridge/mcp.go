package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/ejfkdev/xyz-go/errors"
)

// Version identities this bridge in the MCP client handshake.
const Version = "0.1.0"

// MCPConfig declares a proxied stdio MCP server. The gateway launches the
// command once per process, performs the handshake and tools/list up front,
// and forwards every subsequent call through the official SDK client. The
// server process lives until the gateway exits (Close is offered for
// explicit teardown).
type MCPConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Tools limits proxying to these remote tool names; empty proxies all.
	Tools []string `json:"tools,omitempty"`
	// Prefix renames proxied tools to "<prefix><tool>"; empty falls back to
	// the config tool name plus "." as the namespace.
	Prefix string `json:"prefix,omitempty"`
}

// RemoteTool is one tool discovered via tools/list, the shape consumed by
// ImportToolsList and carried by MCPProxy.
type RemoteTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Input       map[string]any `json:"input,omitempty"` // raw inputSchema subset
}

// MCPProxy is a live proxied server connection. tools maps the displayed
// (namespaced) name to the remote tool description.
type MCPProxy struct {
	cmd     *exec.Cmd
	session *sdkmcp.ClientSession
	tools   map[string]remoteTool
}

type remoteTool struct {
	name        string // 远端原名（call 时使用）
	Description string
	Input       map[string]any
}

// StartMCPProxy launches the server process, completes the MCP handshake
// and lists its tools. Proxied names carry the namespace prefix.
func StartMCPProxy(ctx context.Context, cfg MCPConfig, namespace string) (*MCPProxy, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp.command must not be empty")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("bridge: pipe to %q: %w", cfg.Command, err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("bridge: pipe from %q: %w", cfg.Command, err)
	}
	cmd.Stderr = os.Stderr // 子 server 的日志直通网关 stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("bridge: start mcp server %q: %w", cfg.Command, err)
	}
	p := &MCPProxy{cmd: cmd, tools: map[string]remoteTool{}}
	fail := func(err error) (*MCPProxy, error) {
		p.Close()
		return nil, err
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "xyz-bridge", Version: Version}, nil)
	session, err := client.Connect(ctx, &sdkmcp.IOTransport{Reader: out, Writer: in}, nil)
	if err != nil {
		return fail(fmt.Errorf("bridge: mcp handshake with %q: %w", cfg.Command, err))
	}
	p.session = session
	res, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return fail(fmt.Errorf("bridge: tools/list from %q: %w", cfg.Command, err))
	}
	filter := map[string]bool{}
	for _, t := range cfg.Tools {
		filter[t] = true
	}
	for _, t := range res.Tools {
		if len(filter) > 0 && !filter[t.Name] {
			continue
		}
		p.tools[namespace+t.Name] = remoteTool{
			name:        t.Name,
			Description: t.Description,
			Input:       inputAsMap(t.InputSchema),
		}
	}
	return p, nil
}

// InvokeTool returns an Invoker that forwards calls to one proxied tool.
// Tool-level errors (isError results) surface as internal errors carrying
// the remote text; transport failures surface as unavailable, so the
// gateway inherits the same taxonomy it would produce natively.
func (p *MCPProxy) InvokeTool(display string) Invoker {
	rt := p.tools[display]
	return func(ctx context.Context, args map[string]any) (any, error) {
		res, err := p.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: rt.name, Arguments: args})
		if err != nil {
			return nil, errs.Wrap(errs.KindUnavailable, err)
		}
		if res.IsError {
			return nil, errs.New(errs.KindInternal, toolResultText(res))
		}
		if text := toolResultText(res); text != "" {
			return text, nil
		}
		if res.StructuredContent != nil {
			return res.StructuredContent, nil
		}
		return "", nil
	}
}

// Close kills the server process; the OS would reap it at gateway exit
// anyway, this is the explicit path.
func (p *MCPProxy) Close() {
	if p != nil && p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// toolResultText extracts the concatenated text content of a call result.
func toolResultText(res *sdkmcp.CallToolResult) string {
	var texts []string
	for _, content := range res.Content {
		if tc, ok := content.(*sdkmcp.TextContent); ok && tc.Text != "" {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// inputAsMap normalizes the client-side InputSchema (a map from JSON, or
// raw embedded JSON) to the subset map parseSchema consumes.
func inputAsMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case json.RawMessage:
		if len(m) > 0 {
			var out map[string]any
			if json.Unmarshal(m, &out) == nil {
				return out
			}
		}
	}
	return nil
}

// ImportToolsList parses a tools/list response or a bare tool array into
// remote tool descriptors — the "paste a string" path: combine with an
// invoker (MCPProxy.InvokeTool, or custom) and NewTool to turn a pasted
// listing into three-channel commands with no config file at all.
func ImportToolsList(raw []byte) ([]RemoteTool, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err == nil {
		var arr []any
		if res, ok := root["result"].(map[string]any); ok {
			arr, _ = res["tools"].([]any)
		}
		if arr == nil {
			arr, _ = root["tools"].([]any)
		}
		if arr == nil {
			return nil, fmt.Errorf("bridge: no tools array found in payload")
		}
		return parseToolArray(arr)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := make([]RemoteTool, 0, len(arr))
		for _, t := range arr {
			out = append(out, remoteFromMap(t))
		}
		return out, nil
	}
	return nil, fmt.Errorf("bridge: payload is neither a tools/list result nor a tool array")
}

func parseToolArray(arr []any) ([]RemoteTool, error) {
	out := make([]RemoteTool, 0, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("bridge: tool #%d is not an object", i)
		}
		out = append(out, remoteFromMap(m))
	}
	return out, nil
}

func remoteFromMap(m map[string]any) RemoteTool {
	rt := RemoteTool{}
	rt.Name, _ = m["name"].(string)
	rt.Description, _ = m["description"].(string)
	rt.Input = inputAsMap(m["inputSchema"])
	return rt
}
