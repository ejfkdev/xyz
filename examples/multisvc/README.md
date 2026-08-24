# examples/multisvc — 多个 CLI 组成同一个服务

一份配置本身就是"多个 CLI → 一个服务"：tools 列表里的每一条都落进同一个
CLI 树、同一个 HTTP 路由表和同一台 MCP 服务。这个目录展示两种组织形态，
都用 **不带点分的工具名**（顶层命令）：

## 形态一：每个 CLI 一个文件，合并成服务

`ls.json`、`whoami.json`、`du.json` 各自只声明一条命令；运行时把多个源
合并进同一个注册表（`LoadMany`，或 XYZ_CONFIG 用逗号分隔）：

```console
$ XYZ_CONFIG=ls.json,whoami.json,du.json xyz -h
$ XYZ_CONFIG=ls.json,whoami.json,du.json xyz ls --path /tmp
$ XYZ_CONFIG=ls.json,whoami.json,du.json xyz whoami
$ XYZ_CONFIG=ls.json,whoami.json,du.json xyz serve      # 一台服务挂全部三个
```

同库用法：

```go
reg, err := bridge.LoadMany(ctx, []string{"ls.json", "whoami.json", "du.json"})
```

跨源出现同名工具会在启动时报错（不拖到第一次调用）。

## 形态二：一个文件声明多个工具 + 自定义路由前缀

`combined.json` 把三个命令放进同一份配置，并用 `http_prefix: "/api"` 把
HTTP 路由前缀从默认的 `/tools` 换成 `/api`：

```console
$ xyz serve            # 用 combined.json（复制为 xyz.json 或设 XYZ_CONFIG）
$ curl -X POST localhost:8080/api/whoami
$ curl -X POST localhost:8080/api/ls -H 'Content-Type: application/json' -d '{"path":"/tmp"}'
$ curl -X POST localhost:8080/api/du  -H 'Content-Type: application/json' -d '{}'
```

同一服务下 CLI 形态照常（不带前缀概念）：`xyz ls`、`xyz whoami`、`xyz du`；
MCP 形态的工具名就是 `ls`、`whoami`、`du`。

注意：不带点分的名字可以做顶层命令，但如果多个源里混用了 `ls`（顶层）与
`fs.ls`（分组），两者互不冲突——点分是层级、不分点是顶层，各归各位。