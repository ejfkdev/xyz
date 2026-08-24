# xyz — 外部工具，三界面

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev/dl/)
[![MCP Protocol](https://img.shields.io/badge/MCP-2024%E2%80%932026--07--28-0764e0?style=flat)](https://modelcontextprotocol.io/specification/2026-07-28)
[![Dependencies](https://img.shields.io/badge/依赖-仅_xyz--go_与官方_SDK-2ea44f?style=flat)](#依赖与限制)

构建在 [xyz-go](https://github.com/ejfkdev/xyz-go) 之上的桥接层：**不用写任何 Go 代码**，凭一份声明式配置文件（`xyz.json`）把现有命令行程序、远程 MCP 服务或粘贴进来的 `tools/list` 清单包装成 xyz 命令。每条声明出的工具立即获得与 xyz-go 原生定义相同的三条通道：**CLI 子命令**、**HTTP REST 服务**（附 OpenAPI 文档）与 **MCP 工具服务**。

```
                    xyz.json ──┐
  粘贴的 tools/list 清单 ──┤──► bridge ──► registry ──►  CLI  /  HTTP  /  MCP
        远程 MCP 服务 ──────┘                  （xyz-go 三个前端，未作改动）
```

`xyz` 网关每次进程启动时解析一次配置：执行一条命令后退出，或者用 `xyz mcp stdio` / `xyz serve` 长期驻留。

## 快速开始

```console
$ go build -o xyz ./cmd/xyz          # 把 examples/xyz.json 放到它旁边
$ xyz -h                              # xyz.json 里的工具全部实时转成 CLI 帮助
$ xyz sys echo --msg "hello"          # exec：/bin/echo hello
$ xyz dns lookup --host example.com   # exec：dig +short A example.com
$ xyz serve                           # 长期形态：HTTP（POST /tools/... + /openapi.json）
$ xyz mcp stdio                       # 长期形态：MCP 服务（官方 SDK）
```

配置是工具清单，每条工具由恰好一个适配器承载：

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

## 适配器

### `exec` — 任意命令行程序

每次调用起一次进程，注入参数，把 stdout 修剪后作为文本返回（刻意不做输出解析）。参数来自 `input` schema。

| 字段 | 含义 | 默认 |
| --- | --- | --- |
| `program` | 要执行的可执行文件 | 必填 |
| `args` | argv 模板，`{name}` 占位符从入参插值 | — |
| `env` | 追加的环境变量，值同样可插值 | 继承父环境 |
| `timeout` | 单次调用的时限（Go duration 语法） | `30s` |
| `max_output` | stdout 上限（字节） | 1 MiB |

argv 模板规则：

- 恰好为 `{name}` 的元素，入参是布尔类型时：true → `--name`，false → 删去该元素；其他类型 → 值的字符串形式；
- 含 `{name}` 的元素逐个替换（数组以 `,` 连接）；任一引用的入参缺席时**整元素删除**——可选 flag 白得；
- 占位符拼写错误在配置解析期被拒绝，而不是拖到第一次调用。

退出行为：退出码 0 → stdout 文本；非零 → 调用失败并携带程序的 stderr，归类 `internal`；超时或启动失败 → `unavailable`。映射复用 xyz-go 的错误分类表，CLI 退出码、HTTP 状态码、JSON-RPC 错误码三者保持一致。

### `mcp` — 代理一台 stdio MCP 服务

`command`/`args` 在网关进程内启动一次服务进程；bridge 完成 MCP 握手、启动时就地拉取 tools/list，之后每次调用经[官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk) 客户端转发。`tools` 限定向命名的子集转发（空 = 全部）；代理出的名字以 `<前缀><工具名>` 命名（`prefix` 或配置工具名加 `.`）。

```json
{ "name": "repomix", "mcp": { "command": "npx", "args": ["-y", "@some/repo-mcp"], "tools": ["analyze"] } }
```

### 粘贴字符串（库用法）

`bridge.ImportToolsList` 解析 tools/list 响应或裸工具数组，产出 `RemoteTool` 描述；`bridge.NewTool` 把一份 JSON Schema + 一个 `Invoker` 变成完整接线的 `*spec.Entry`。粘贴一张清单、提供一个执行器、注册进 registry —— 三条通道齐活，无需结构体：

```go
tools, err := bridge.ImportToolsList(pasted) // {"result":{"tools":[...]}}
for _, rt := range tools {
    e, err := bridge.NewTool(rt.Name, rt.Description, rt.Description, rt.Input, myInvoker)
    // reg.Add(e) → CLI / HTTP / MCP
}
```

## 输入 schema 子集

动态工具按下述子集校验（draft-07 风味）：`object`（含 `properties`/`required`）与标量 `string`/`integer`/`number`/`boolean`/`array`（含 `items`），外加 `description`、`default`、`enum`（仅标量）。嵌套对象在 MCP 与 HTTP（JSON body）可用，CLI 暂无嵌套 flag 语法、自动跳过。宽松输入按 xyz-go 定义时的口径归一：CLI flag 与 HTTP query 的字符串会转成 schema 类型，`--port 8080` 落到手即整数 `8080`。

## 配置加载

查找顺序：环境变量 `XYZ_CONFIG`（本地路径**或** `http(s)` URL——每次运行重新拉取），否则当前目录的 `./xyz.json`。网关每次进程解析一次：`xyz <工具> …` 调用完即退；`xyz mcp stdio` / `xyz serve` 把注册表留在进程生命周期内。

## 名字到三条通道的映射

点分工具名（如 `dns.lookup`）按各接口的习语落地：

- **CLI**：点分即子命令层级 —— `xyz dns lookup --host example.com`；点分名保留在配置与 help 输出里；
- **HTTP**：`POST /tools/dns/lookup`，JSON body 与 query 绑定合并；附赠 `/healthz` 与 `/openapi.json`；
- **MCP**：工具名即 `dns.lookup`，参数由 schema 定型。

## 依赖与限制

依赖仅 `xyz-go` 与官方 `go-sdk`（代理用客户端），无其他三方包；配置解析用标准库 JSON。

已知边界（有意为之）：exec 工具返回文本（stdout），不做结果解析；每次调用起一个进程（长期形态请用服务）；MCP 代理进程随网关存活至退出；schema 子集与 OpenAPI 导入路径（`ImportOpenAPI` 是下一站）刻意收小——子集之外的内容在解析期即被拒绝，而不是误执行。

## 许可证

MIT