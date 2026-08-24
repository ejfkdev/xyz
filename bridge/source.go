package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ejfkdev/xyz-go/registry"
)

// DefaultConfigName is the file the gateway looks for in the current
// directory when XYZ_CONFIG is unset.
const DefaultConfigName = "xyz.json"

// Load reads the config from a local path or an http(s) URL and builds the
// registry. An empty source falls back to LoadDefault's lookup order. URLs
// are fetched afresh on every process start — the "resolve once per run"
// model the gateway documents.
func Load(ctx context.Context, source string) (*registry.Registry, error) {
	if source == "" {
		return LoadDefault(ctx)
	}
	data, err := readSource(source)
	if err != nil {
		return nil, err
	}
	return ParseAndBuild(ctx, data)
}

// LoadDefault locates the config by convention: XYZ_CONFIG (a path or URL)
// wins, otherwise ./xyz.json in the current directory.
func LoadDefault(ctx context.Context) (*registry.Registry, error) {
	if p := os.Getenv("XYZ_CONFIG"); p != "" {
		return Load(ctx, p)
	}
	data, err := os.ReadFile(DefaultConfigName)
	if err != nil {
		return nil, fmt.Errorf("bridge: no %s in the current directory (set XYZ_CONFIG to a file or URL to override): %w", DefaultConfigName, err)
	}
	return ParseAndBuild(ctx, data)
}

// ParseAndBuild parses an xyz.json document and resolves its tools.
func ParseAndBuild(ctx context.Context, data []byte) (*registry.Registry, error) {
	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, err
	}
	return cfg.Build(ctx)
}

// ParseConfig parses the xyz.json document shape.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("bridge: parse config: %w", err)
	}
	return &cfg, nil
}

const maxConfigBytes = 4 << 20 // 远端配置大小上限

func readSource(source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("bridge: read %s: %w", source, err)
		}
		return data, nil
	}
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(source)
	if err != nil {
		return nil, fmt.Errorf("bridge: fetch %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bridge: fetch %s: HTTP %s", source, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes))
	if err != nil {
		return nil, fmt.Errorf("bridge: fetch %s: %w", source, err)
	}
	return data, nil
}
