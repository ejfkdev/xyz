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
)

// TestExamplesSmoke 逐条真实调用 examples/xyz.json 里除外部网络外的全部
// 工具，保证示例配置始终可用，而不只是存在于 README。
func TestExamplesSmoke(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("examples/xyz.json 按 macOS 命令路径书写")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	md5File := filepath.Join(dir, "md5.txt")
	if err := os.WriteFile(md5File, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-curl"))
	}))
	defer httpSrv.Close()

	data, err := os.ReadFile(filepath.Join("..", "examples", "xyz.json"))
	if err != nil {
		t.Fatalf("read examples/xyz.json: %v", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	reg, err := cfg.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const (
		sha256ABC = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
		md5ABC    = "900150983cd24fb0d6963f7d28e17f72"
	)
	cases := []struct {
		name  string
		args  map[string]any
		check func(t *testing.T, out string)
	}{
		{"sys.echo", map[string]any{"msg": "测试"}, equal("测试")},
		{"sys.date", nil, nonEmpty},
		{"sys.sha256", map[string]any{"text": "abc"}, equal(sha256ABC)},
		{"sys.uname", nil, nonEmpty},
		{"sys.uptime", nil, nonEmpty},
		{"sys.whoami", nil, nonEmpty},
		{"sys.hostname", nil, nonEmpty},
		{"fs.ls", map[string]any{"path": dir}, contains("sample.txt")},
		{"fs.du", map[string]any{"path": dir}, nonEmpty},
		{"fs.md5", map[string]any{"file": md5File}, equal(md5ABC)},
		{"fs.head", map[string]any{"file": file, "lines": "1"}, equal("alpha")},
		{"fs.wc", map[string]any{"file": file}, contains("3")},
		{"net.ping", map[string]any{"host": "127.0.0.1", "count": "1"}, nonEmpty},
		{"net.curl", map[string]any{"url": httpSrv.URL}, equal("hello-curl")},
	}
	// dns.lookup 依赖本机 resolver 提供 localhost 的 A 记录（dig 不走
	// /etc/hosts，部分环境查不到）——环境支持时才纳入断言。
	if localhostResolves(t) {
		cases = append(cases, struct {
			name  string
			args  map[string]any
			check func(t *testing.T, out string)
		}{"dns.lookup", map[string]any{"host": "localhost"}, contains("127.0.0.1")})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := reg.Get(c.name)
			if !ok {
				t.Fatalf("%s not registered (names: %v)", c.name, reg.Names())
			}
			out, err := e.Invoke(context.Background(), c.args)
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			s, ok := out.(string)
			if !ok {
				t.Fatalf("out = %#v, want string", out)
			}
			c.check(t, s)
		})
	}
}

func nonEmpty(t *testing.T, out string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		t.Error("empty output")
	}
}

// localhostResolves 探测本机 resolver 是否给出 localhost 的 A 记录。
func localhostResolves(t *testing.T) bool {
	t.Helper()
	inv := ExecInvoker(ExecConfig{Program: "/usr/bin/dig", Args: []string{"+short", "A", "localhost"}, Timeout: "5s"})
	out, err := inv(context.Background(), nil)
	if err != nil {
		return false
	}
	return strings.Contains(out.(string), "127.0.0.1")
}

func equal(want string) func(*testing.T, string) {
	return func(t *testing.T, out string) {
		t.Helper()
		if strings.TrimSpace(out) != want {
			t.Errorf("out = %q, want %q", out, want)
		}
	}
}

func contains(sub string) func(*testing.T, string) {
	return func(t *testing.T, out string) {
		t.Helper()
		if !strings.Contains(out, sub) {
			t.Errorf("out = %q, want it to contain %q", out, sub)
		}
	}
}
