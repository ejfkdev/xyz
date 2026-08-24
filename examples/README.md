# examples/xyz.json — 用系统常用命令做的实操清单

这份配置把 macOS 系统自带的 17 个常用命令包装成 xyz 工具，覆盖四组场景。复制到任意目录（或设 `XYZ_CONFIG` 指向它）即可试玩：

```console
$ xyz -h                                    # 四组全部转成 CLI 帮助
$ xyz sys uname                             # /usr/bin/uname -a
$ xyz sys sha256 --text abc                 # shasum -a 256
$ xyz fs ls                                # 默认当前目录，--path 可换
$ xyz fs head --file README.md --lines 3
$ xyz net ping --host 127.0.0.1 --count 1
$ xyz dns lookup --host example.com --qtype MX
```

| 分组 | 工具 | 包装的命令 | 说明 |
| --- | --- | --- | --- |
| sys | `sys.echo` | `echo {msg}` | 演示最简 exec + 文本回显 |
| sys | `sys.date` | `date +"%Y-%m-%d %H:%M:%S"` | 无参工具形态 |
| sys | `sys.sha256` | `sh -c "printf … \| shasum -a 256 …"` | 演示 shell 管道型 argv |
| sys | `sys.uname` / `sys.uptime` / `sys.whoami` / `sys.hostname` | 同名命令 | 零参数信息工具 |
| fs | `fs.ls` / `fs.du` | `ls -la {path}` / `du -sh {path}` | `default` 让参数可省略 |
| fs | `fs.md5` | `md5 -q {file}` | 单必填参数 |
| fs | `fs.head` / `fs.wc` | `head -n {lines} {file}` / `wc -l {file}` | 整数参数 + 多占位符模板 |
| net | `net.ping` | `ping -c {count} {host}` | 整数默认值 |
| net | `net.curl` | `curl -s {url}` | 把任意 URL 变成工具 |
| net | `net.public-ip` | `curl -s https://ipinfo.io/ip` | 外网示例（需要网络） |
| dns | `dns.lookup` | `dig +short {qtype} {host}` | `enum` 限制记录类型 |
| dns | `dns.whois` | `whois {domain}` | 外网示例（需要网络） |

注意：路径按 macOS `/bin`、`/usr/bin`、`/sbin` 写的（`md5 -q`、`ping -c` 都是 macOS 方言）。Linux 请按需替换，例如 `md5` → `md5sum`、`ping -c` 同名但 `md5 `特性不同；BSD 之外的 `shasum -a 256` → `sha256sum`。

外网类（`net.public-ip`、`dns.whois`）未纳入自动冒烟测试，其余 14 个工具在 `bridge/examples_test.go` 里逐条真实调用。