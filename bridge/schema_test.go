package bridge

import (
	"testing"

	errs "github.com/ejfkdev/xyz-go/errors"
)

func mustParse(t *testing.T, raw any) *schemaNode {
	t.Helper()
	n, err := parseSchema(raw)
	if err != nil {
		t.Fatalf("parseSchema: %v", err)
	}
	return n
}

func TestSchemaCoercions(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
			"ratio": map[string]any{"type": "number"},
			"deep":  map[string]any{"type": "boolean"},
			"tag":   map[string]any{"type": "string"},
			"list":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	})
	// 三种通道的宽松输入都归一：CLI/HTTP 传字符串，MCP 传 JSON 数值。
	got, err := n.normalize(map[string]any{
		"count": "42",
		"ratio": "1.5",
		"deep":  "1",
		"tag":   float64(7),
		"list":  "a, b",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["count"] != int64(42) {
		t.Errorf("count = %#v, want int64(42)", got["count"])
	}
	if got["ratio"] != 1.5 {
		t.Errorf("ratio = %#v, want 1.5", got["ratio"])
	}
	if got["deep"] != true {
		t.Errorf("deep = %#v, want true", got["deep"])
	}
	if got["tag"] != "7" {
		t.Errorf("tag = %#v, want \"7\"", got["tag"])
	}
	if l, ok := got["list"].([]any); !ok || len(l) != 2 || l[0] != "a" || l[1] != "b" {
		t.Errorf("list = %#v, want [a b]", got["list"])
	}

	// 类型不符聚合为一条 invalid_input，不panic。
	if _, err := n.normalize(map[string]any{"count": "3.5"}); err == nil {
		t.Error("count=3.5 accepted, want error")
	} else if errs.Classify(err) != errs.KindInvalidInput {
		t.Errorf("classify = %q, want invalid_input", errs.Classify(err))
	}
	if _, err := n.normalize(map[string]any{"deep": "maybe"}); err == nil {
		t.Error("deep=maybe accepted, want error")
	}
}

func TestSchemaDefaultsAndRequired(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"host": map[string]any{"type": "string"},
			"mode": map[string]any{"type": "string", "default": "fast"},
		},
		"required": []any{"host"},
	})
	got, err := n.normalize(map[string]any{"host": "h"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["mode"] != "fast" {
		t.Errorf("mode = %#v, want default \"fast\"", got["mode"])
	}
	got, err = n.normalize(map[string]any{"host": "h", "mode": "slow"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["mode"] != "slow" {
		t.Errorf("mode = %#v, want override \"slow\"", got["mode"])
	}
	if _, err := n.normalize(map[string]any{}); err == nil {
		t.Error("missing required host accepted")
	}
}

func TestSchemaEnum(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
			"n":    map[string]any{"type": "integer", "enum": []any{float64(1), float64(2)}},
		},
	})
	if _, err := n.normalize(map[string]any{"mode": "fast", "n": "2"}); err != nil {
		t.Fatalf("valid enum rejected: %v", err)
	}
	if _, err := n.normalize(map[string]any{"mode": "instant", "n": float64(2)}); err == nil {
		t.Error("bad string enum accepted")
	}
	if _, err := n.normalize(map[string]any{"mode": "fast", "n": "3"}); err == nil {
		t.Error("bad numeric enum accepted")
	}
}

func TestSchemaUnknownKeysPassThrough(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	got, err := n.normalize(map[string]any{"extra": "kept"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["extra"] != "kept" {
		t.Errorf("extra = %#v, want passthrough", got["extra"])
	}
}

func TestSchemaNestedObject(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cfg": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verbose": map[string]any{"type": "boolean"},
				},
				"required": []any{"verbose"},
			},
		},
	})
	got, err := n.normalize(map[string]any{"cfg": map[string]any{"verbose": "true"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	inner, ok := got["cfg"].(map[string]any)
	if !ok || inner["verbose"] != true {
		t.Errorf("cfg = %#v, want nested normalize", got["cfg"])
	}
	if _, err := n.normalize(map[string]any{"cfg": map[string]any{}}); err == nil {
		t.Error("nested required missing accepted")
	}
}

func TestSchemaParseRejects(t *testing.T) {
	cases := []map[string]any{
		{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "regex"}}},
		{"type": "object", "required": []any{"ghost"}},
		{"type": "object", "properties": map[string]any{"a": "not-an-object"}},
		{"type": "array", "items": "string"},
		{"type": "string", "enum": []any{}},
		{"type": "object", "enum": []any{"x"}},
	}
	for i, c := range cases {
		if _, err := parseSchema(c); err == nil {
			t.Errorf("case %d (%v) accepted", i, c)
		}
	}
	// nil schema = 无参工具
	n, err := parseSchema(nil)
	if err != nil || n.typ != "object" {
		t.Errorf("nil schema: %v, %v", n, err)
	}
}

func TestSchemaImplicitObject(t *testing.T) {
	// properties 显隐 type: object（写配置时少打一个键）。
	n := mustParse(t, map[string]any{
		"properties": map[string]any{"a": map[string]any{"type": "string"}},
	})
	if n.typ != "object" || len(n.props) != 1 {
		t.Errorf("n = %+v", n)
	}
}

func TestSchemaDefaultCoercion(t *testing.T) {
	// default 的 JSON 形态随类型归一：数字是 float64、字符串直通。
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"port":  map[string]any{"type": "integer", "default": float64(8080)},
			"ratio": map[string]any{"type": "number", "default": 0.5},
			"flag":  map[string]any{"type": "boolean", "default": false},
		},
	})
	got, err := n.normalize(map[string]any{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got["port"] != int64(8080) {
		t.Errorf("port = %#v, want int64(8080)", got["port"])
	}
	if got["ratio"] != 0.5 {
		t.Errorf("ratio = %#v, want 0.5", got["ratio"])
	}
	if got["flag"] != false {
		t.Errorf("flag = %#v, want false", got["flag"])
	}
}

func TestSchemaArrayItemsInteger(t *testing.T) {
	n := mustParse(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ports": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		},
	})
	got, err := n.normalize(map[string]any{"ports": "80, 443"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	l, ok := got["ports"].([]any)
	if !ok || len(l) != 2 || l[0] != int64(80) || l[1] != int64(443) {
		t.Errorf("ports = %#v, want [80 443] as integers", got["ports"])
	}
	if _, err := n.normalize(map[string]any{"ports": "80,http"}); err == nil {
		t.Error("non-integer array element accepted")
	}
}
