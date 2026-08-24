package bridge

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/ejfkdev/xyz-go/registry"
	"github.com/ejfkdev/xyz-go/spec"
)

// Invoker runs one tool call with schema-normalized arguments and returns
// the value every frontend renders: plain text for exec tools, a native
// JSON value for MCP proxies.
type Invoker func(ctx context.Context, args map[string]any) (any, error)

// ToolOpts tunes the per-tool channel bindings of a dynamic entry.
type ToolOpts struct {
	// HTTPPrefix replaces the default "/tools" route prefix; leading and
	// trailing slashes are normalized away. The full route becomes
	// "<method> /<prefix>/<dotted-path>".
	HTTPPrefix string
	// MCPName overrides the MCP tool name offered to clients; empty keeps
	// the entry name. HTTP and CLI naming is never affected, so each
	// channel can carry its own prefix independently.
	MCPName string
	// CLISkip removes the whole command from the CLI frontend.
	CLISkip bool
}

// NewTool is the dynamic entry factory with default channel bindings; see
// NewToolWith for the tunables.
func NewTool(name, summary, description string, input any, inv Invoker) (*spec.Entry, error) {
	return NewToolWith(name, summary, description, input, inv, ToolOpts{})
}

// NewToolWith is the dynamic entry factory: a JSON Schema subset (input)
// plus an Invoker becomes a fully wired spec.Entry — the CLI subcommand
// (dotted segments become levels), the HTTP route and the MCP tool all
// derive from it, with no compile-time struct. A dotted-free name lands as
// a top-level command: HTTP POST /<prefix>/ls, CLI <app> ls.
func NewToolWith(name, summary, description string, input any, inv Invoker, opts ToolOpts) (*spec.Entry, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("bridge: tool name must not be empty")
	}
	if inv == nil {
		return nil, fmt.Errorf("bridge: tool %q: nil invoker", name)
	}
	node, err := parseSchema(input)
	if err != nil {
		return nil, fmt.Errorf("bridge: tool %q: %w", name, err)
	}
	if node.typ != "object" {
		return nil, fmt.Errorf("bridge: tool %q: input schema must be an object, got %q", name, node.typ)
	}

	prefix := strings.Trim(opts.HTTPPrefix, "/")
	if prefix == "" {
		prefix = "tools"
	}
	e := &spec.Entry{
		Name:        name,
		Summary:     summary,
		Description: description,
		InputSchema: toSpecSchema(node),
		Root:        toFieldMeta(name, node),
		CLI:         spec.CliHints{Skip: opts.CLISkip},
		MCP:         spec.MCPHints{Name: opts.MCPName},
		HTTP: spec.HTTPHints{
			Method: "POST",
			Path:   "/" + prefix + "/" + strings.ReplaceAll(name, ".", "/"),
		},
	}
	e.Invoke = func(ctx context.Context, args map[string]any) (any, error) {
		norm, err := node.normalize(args)
		if err != nil {
			return nil, err
		}
		return inv(ctx, norm)
	}
	return e, nil
}

// toFieldMeta builds the argument tree the CLI and HTTP frontends read.
// Kinds come straight from the schema types; nested object properties stay
// visible to MCP and HTTP but are skipped by the CLI frontend, which has no
// nested-flag syntax yet.
func toFieldMeta(name string, n *schemaNode) *spec.FieldMeta {
	root := &spec.FieldMeta{
		Name:     name,
		JSONName: name,
		Type:     reflect.TypeOf(map[string]any(nil)),
		Kind:     reflect.Struct,
	}
	for _, pname := range sortedKeys(n.props) {
		sub := n.props[pname]
		f := &spec.FieldMeta{
			Name:        pname,
			JSONName:    pname,
			Description: sub.desc,
			Required:    n.required[pname],
			Enum:        sub.enum,
			Default:     sub.def,
		}
		if sub.def == nil {
			f.Default = nil
		}
		setFieldKind(f, sub)
		root.Fields = append(root.Fields, f)
	}
	return root
}

// setFieldKind maps a schema type onto the reflect metadata the xyz-go
// frontends use to pick flag kinds (bool/slice/string) and binders.
func setFieldKind(f *spec.FieldMeta, n *schemaNode) {
	switch n.typ {
	case "string":
		f.Kind, f.Type = reflect.String, reflect.TypeOf("")
	case "integer":
		f.Kind, f.Type = reflect.Int64, reflect.TypeOf(int64(0))
	case "number":
		f.Kind, f.Type = reflect.Float64, reflect.TypeOf(float64(0))
	case "boolean":
		f.Kind, f.Type = reflect.Bool, reflect.TypeOf(false)
	case "array":
		f.Kind, f.Type = reflect.Slice, reflect.TypeOf([]string(nil))
		el := &spec.FieldMeta{}
		if n.items == nil {
			el.Kind, el.Type = reflect.String, reflect.TypeOf("")
		} else {
			setFieldKind(el, n.items)
		}
		f.Elem = el
		if el.Kind == reflect.Struct || el.Kind == reflect.Slice || el.Kind == reflect.Ptr {
			f.CLI.Skip = true // CLI 前端不支持嵌套元素的 flag
		}
	case "object":
		f.Kind, f.Type = reflect.Struct, reflect.TypeOf(map[string]any(nil))
		f.CLI.Skip = true
	}
}

// toSpecSchema converts the parsed subset into the spec.Schema form that
// the MCP inputSchema and the OpenAPI document consume.
func toSpecSchema(n *schemaNode) *spec.Schema {
	s := &spec.Schema{
		Type:        "object",
		Description: n.desc,
		Properties:  map[string]*spec.Schema{},
	}
	for _, name := range sortedKeys(n.props) {
		s.Properties[name] = nodeSchema(n.props[name])
	}
	if len(n.required) > 0 {
		s.Required = sortedKeys(n.required)
	}
	if len(s.Properties) == 0 {
		s.Properties = nil
	}
	return s
}

func nodeSchema(n *schemaNode) *spec.Schema {
	s := &spec.Schema{Type: n.typ, Description: n.desc, Enum: n.enum, Default: n.def}
	switch {
	case n.typ == "array" && n.items != nil:
		s.Items = nodeSchema(n.items)
	case n.typ == "object":
		s.Properties = map[string]*spec.Schema{}
		for _, name := range sortedKeys(n.props) {
			s.Properties[name] = nodeSchema(n.props[name])
		}
		if len(n.required) > 0 {
			s.Required = sortedKeys(n.required)
		}
	}
	return s
}

// Config is the xyz.json document: a declarative list of tools, each
// carried by exactly one adapter. One config hosts as many tools as needed
// — they compose into a single registry, one service behind every frontend.
type Config struct {
	// HTTPPrefix sets the route prefix for every tool of this config
	// (default "/tools"); "/api" turns them into /api/<dotted-path>.
	HTTPPrefix string `json:"http_prefix,omitempty"`
	// MCPPrefix sets the MCP tool-name prefix for every tool of this
	// config: tools are offered as <mcp_prefix><name>. Independent from
	// HTTPPrefix — set one, the other, or both, per channel's needs.
	MCPPrefix string       `json:"mcp_prefix,omitempty"`
	Tools     []ToolConfig `json:"tools"`
}

// ToolConfig declares one dynamic tool.
type ToolConfig struct {
	Name        string         `json:"name"`
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description,omitempty"`
	Input       map[string]any `json:"input,omitempty"` // JSON Schema subset for the arguments
	Exec        *ExecConfig    `json:"exec,omitempty"`  // local command adapter
	MCP         *MCPConfig     `json:"mcp,omitempty"`   // stdio MCP server proxy
	// MCPName pins this exec tool's MCP tool name outright (beats
	// Config.MCPPrefix). Proxied MCP tools use Config.MCPPrefix instead —
	// one proxy yields many tools, one pinned name cannot cover them.
	MCPName string `json:"mcp_name,omitempty"`
}

// Build resolves every declared tool into entries registered in a fresh
// registry. Exec tools bind immediately; MCP proxies launch their server
// processes here — once per process — and stay alive until it exits.
func (c *Config) Build(ctx context.Context) (*registry.Registry, error) {
	reg := registry.New()
	if c == nil {
		return reg, nil
	}
	for i := range c.Tools {
		tc := &c.Tools[i]
		if err := tc.validate(); err != nil {
			return nil, err
		}
		base := ToolOpts{HTTPPrefix: c.HTTPPrefix}
		switch {
		case tc.Exec != nil:
			if err := tc.Exec.validate(tc.Input); err != nil {
				return nil, fmt.Errorf("bridge: tool %q: %w", tc.Name, err)
			}
			opts := base
			opts.MCPName = tc.MCPName
			if opts.MCPName == "" && c.MCPPrefix != "" {
				opts.MCPName = c.MCPPrefix + tc.Name
			}
			e, err := NewToolWith(tc.Name, tc.Summary, tc.Description, tc.Input, ExecInvoker(*tc.Exec), opts)
			if err != nil {
				return nil, err
			}
			if err := reg.Add(e); err != nil {
				return nil, fmt.Errorf("bridge: tool %q: %w", tc.Name, err)
			}
		case tc.MCP != nil:
			namespace := tc.MCP.Prefix
			if namespace == "" {
				namespace = tc.Name + "."
			}
			proxy, err := StartMCPProxy(ctx, *tc.MCP, namespace)
			if err != nil {
				return nil, fmt.Errorf("bridge: tool %q: %w", tc.Name, err)
			}
			for _, display := range sortedKeys(proxy.tools) {
				rt := proxy.tools[display]
				opts := base
				if c.MCPPrefix != "" {
					opts.MCPName = c.MCPPrefix + display
				}
				e, err := NewToolWith(display, firstLine(rt.Description), rt.Description, rt.Input, proxy.InvokeTool(display), opts)
				if err != nil {
					return nil, fmt.Errorf("bridge: tool %q: proxied %s: %w", tc.Name, display, err)
				}
				if err := reg.Add(e); err != nil {
					return nil, fmt.Errorf("bridge: tool %q: proxied %s: %w", tc.Name, display, err)
				}
			}
		}
	}
	return reg, nil
}

func (t *ToolConfig) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("bridge: tool without a name")
	}
	if (t.Exec == nil) == (t.MCP == nil) {
		return fmt.Errorf("bridge: tool %q: exactly one of exec or mcp must be set", t.Name)
	}
	return nil
}

// firstLine reduces a (possibly long) description to the summary shown in
// command listings.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
