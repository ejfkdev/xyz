# xyz — Foreign tools, three interfaces

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev/dl/)
[![MCP Protocol](https://img.shields.io/badge/MCP-2024%E2%80%932026--07--28-0764e0?style=flat)](https://modelcontextprotocol.io/specification/2026-07-28)
[![Dependencies](https://img.shields.io/badge/deps-xyz--go_%2B_official_SDK_only-2ea44f?style=flat)](#dependency-policy)
[![中文](https://img.shields.io/badge/中文-README.zh--CN.md-red?style=flat)](README.zh-CN.md)

A bridge layer on top of [xyz-go](https://github.com/ejfkdev/xyz-go): **foreign tool surfaces are wrapped without writing any Go code**, through one declarative file (`xyz.json`) that binds existing command-line programs, remote MCP servers, or pasted `tools/list` payloads into xyz commands. Every declared tool immediately speaks the same three interfaces an xyz-go definition speaks: **CLI subcommands**, an **HTTP REST service** (with an OpenAPI document), and an **MCP tool server**.

```
                     xyz.json ──┐
  pasted tools/list payload ──┤──► bridge ──► registry ──►  CLI  /  HTTP  /  MCP
        remote MCP server ─────┘                  (xxx-go frontends, unmodified)
```

The `xyz` gateway resolves the config once per process at startup — run a command and exit, or run `xyz mcp stdio` / `xyz serve` and serve long-lived.

## Quick start

```console
$ go build -o xyz ./cmd/xyz          # copy examples/xyz.json next to it
$ xyz -h                              # all tools, converted to CLI, from xyz.json
$ xyz sys echo --msg "hello"          # exec: /bin/echo hello
$ xyz dns lookup --host example.com   # exec: dig +short A example.com
$ xyz serve                           # long-lived HTTP: POST /tools/... + /openapi.json
$ xyz mcp stdio                       # long-lived MCP server (official SDK)
```

A config is a tool list; each tool is carried by exactly one adapter:

```json
{
  "tools": [
    {
      "name": "dns.lookup",
      "summary": "DNS 记录查询",
      "exec": {
        "program": "dig",
        "args": ["+short", "{qtype}", "{host}"],
        "timeout": "15s"
      },
      "input": {
        "type": "object",
        "properties": {
          "host":  { "type": "string", "description": "域名" },
          "qtype": { "type": "string", "enum": ["A", "AAAA", "MX", "TXT"], "default": "A" }
        },
        "required": ["host"]
      }
    },
    {
      "name": "repomix",
      "mcp": { "command": "npx", "args": ["-y", "@some/repo-mcp"] }
    }
  ]
}
```

## Adapters

### `exec` — any command-line program

Every call spawns the process once, feeds the arguments, and returns the trimmed stdout as text (no output parsing, by design). Arguments come from the `input` schema.

| Field | Meaning | Default |
| --- | --- | --- |
| `program` | executable to run | required |
| `args` | argv template, `{name}` placeholders interpolated from inputs | — |
| `env` | extra environment entries, values also interpolated | inherited environment |
| `timeout` | per-invocation bound (Go duration) | `30s` |
| `max_output` | stdout cap in bytes | 1 MiB |

Argv template rules:

- an element that is exactly `{name}` with a **boolean** input becomes `--name` when true and is dropped when false; any other type becomes the value's string form;
- elements containing `{name}` replace each occurrence (slices join with `,`); if any referenced input is absent the **whole element is dropped** — optional flags for free;
- placeholder typos are rejected at config parse time, not at first call.

Exit behavior: exit code 0 → stdout text; non-zero → the call fails carrying the program's stderr, classified `internal`; timeout or spawn failure → `unavailable`. The mapping rides xyz-go's shared error taxonomy, so CLI exit codes, HTTP status codes and JSON-RPC codes all stay consistent.

### `mcp` — proxy a stdio MCP server

`command`/`args` launch the server process once per gateway process; the bridge performs the MCP handshake, lists its tools at startup, and forwards every call through the [official Go SDK](https://github.com/modelcontextprotocol/go-sdk) client. `tools` limits proxying to a named subset (empty = all), and proxied names are namespaced `<prefix><tool>` (`prefix` or the config tool name plus `.`).

```json
{ "name": "repomix", "mcp": { "command": "npx", "args": ["-y", "@some/repo-mcp"], "tools": ["analyze"] } }
```

### Pasted strings (library)

`bridge.ImportToolsList` parses a `tools/list` response or a bare tool array into `RemoteTool` descriptors; `bridge.NewTool` turns a JSON Schema + an `Invoker` into a fully wired `*spec.Entry`. Paste a listing, supply any invoker, and register it — three channels, no struct:

```go
tools, err := bridge.ImportToolsList(pasted) // {"result":{"tools":[...]}}
for _, rt := range tools {
    e, err := bridge.NewTool(rt.Name, rt.Description, rt.Description, rt.Input, myInvoker)
    // reg.Add(e) → CLI / HTTP / MCP
}
```

## Many CLIs, one service

A config's `tools` list already composes multiple commands into the one registry behind every frontend. Two shapes follow from that:

- **Split files, merged at load**: `XYZ_CONFIG=ls.json,whoami.json,du.json xyz serve` (or `bridge.LoadMany`) merges the sources into one service; duplicate tool names across sources fail at startup.
- **Flat, prefixable routes**: a dotted-free tool name lands as a top-level command — `xyz ls` on the CLI, `POST /tools/ls` over HTTP, `ls` over MCP. `http_prefix` on the config swaps the route prefix: `"http_prefix": "/api"` yields `POST /api/ls`.

`examples/multisvc/` wires both shapes to real system utilities.

## Input schema subset

Dynamic tools validate with the subset below (draft-07 flavored): `object` (with `properties`/`required`) and the scalars `string`/`integer`/`number`/`boolean`/`array` (with `items`), plus `description`, `default` and `enum` (scalars). Nested objects work over MCP and HTTP (JSON body) and are skipped on the CLI, which has no nested-flag syntax yet. Loose inputs are coerced like xyz-go defines: CLI flags and HTTP query strings convert to the schema type, so `--port 8080` lands as the integer `8080`.

## Configuration loading

Lookup order: the `XYZ_CONFIG` environment variable (a path **or** an `http(s)` URL, comma-separated for several sources — fetched afresh per run), otherwise `./xyz.json` in the current directory. The gateway resolves once per process: `xyz <tool> …` shuts down right after the call; `xyz mcp stdio` / `xyz serve` hold the resolved registry for the process lifetime.

## Naming across the three fronts

A dotted tool name like `dns.lookup` maps into each interface idiomatically:

- **CLI**: dotted segments are subcommand levels — `xyz dns lookup --host example.com`; the dotted form stays in your config and in help output;
- **HTTP**: `POST /tools/dns/lookup`, JSON body merges with query binding; `/healthz` and `/openapi.json` come along;
- **MCP**: the tool is offered as `dns.lookup`, arguments typed by the schema.

## Dependency policy & limitations

Dependencies: `xyz-go` (already pinned in `go.mod`) and the official `go-sdk` (client side for proxying). No other third-party imports; config parsing is plain JSON on the standard library.

Known limits, by design: exec tools return text (stdout), not parsed results; each call spawns a process (a long-lived server is the intended fix); MCP proxy processes live until the gateway exits; the schema subset and the OpenAPI import path (`ImportOpenAPI` is the next milestone) are intentionally small — anything outside them is rejected at parse time rather than mis-executed.

## License

MIT