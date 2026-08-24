// xyz 网关：把 xyz.json（当前目录，或 XYZ_CONFIG 指向的文件/URL）里
// 声明的工具转成 xyz-go 的三条通道。每次进程启动解析一次：
//
//	xyz -h                            帮助（含全部转换出的子命令）
//	xyz dns.lookup --host example.com 执行一条 exec/mcp 工具
//	xyz mcp stdio                      长期形态：解析一次的 MCP 服务
//	xyz serve                          长期形态：HTTP 服务（附 /openapi.json）
//
// 版本注入：go build -ldflags "-X github.com/ejfkdev/xyz-go.Version=v0.1.0"
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ejfkdev/xyz-bridge/bridge"
	xyzsdk "github.com/ejfkdev/xyz-go"
)

func main() {
	reg, err := bridge.LoadDefault(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "xyz: %v\n", err)
		os.Exit(2)
	}
	os.Exit(xyzsdk.Run(reg, os.Args[1:]))
}
