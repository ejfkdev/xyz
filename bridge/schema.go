package bridge

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	errs "github.com/ejfkdev/xyz-go/errors"
)

// The supported JSON Schema subset (draft-07 flavored, mirroring what
// spec.Schema carries):
//
//	type: object | string | integer | number | boolean | array
//	object: properties, required, description, default, enum
//	scalars: description, default, enum (enum is scalar-only)
//	array: items (scalar or object)
//
// Everything outside the subset (anyOf, oneOf, $ref, pattern, ...) is
// rejected at parse time so configuration mistakes surface at startup
// rather than at first call.

// schemaNode is the parsed form of one node of the subset above.
type schemaNode struct {
	typ      string // subset type name; "object" at the tool input root
	desc     string
	props    map[string]*schemaNode
	required map[string]bool
	enum     []any
	def      any
	items    *schemaNode // array element schema; defaults to string
}

const supportedTypes = "object|string|integer|number|boolean|array"

// parseSchema parses one tool input schema. nil or an empty object mean
// "no declared input" and yield an empty object schema (a parameterless
// tool) — JSON un-marshals an absent "input" key into a zero map, so both
// shapes must land here.
func parseSchema(raw any) (*schemaNode, error) {
	if raw == nil {
		return emptyObjectNode(), nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema must be a JSON object")
	}
	if len(m) == 0 {
		return emptyObjectNode(), nil
	}
	return parseNode(m, "")
}

func emptyObjectNode() *schemaNode {
	return &schemaNode{
		typ:      "object",
		props:    map[string]*schemaNode{},
		required: map[string]bool{},
	}
}

func parseNode(m map[string]any, path string) (*schemaNode, error) {
	n := &schemaNode{props: map[string]*schemaNode{}, required: map[string]bool{}}
	if t, ok := m["type"].(string); ok {
		if !strings.Contains("|"+supportedTypes+"|", "|"+t+"|") {
			return nil, fmt.Errorf("%s: unsupported type %q (want %s)", path, t, supportedTypes)
		}
		n.typ = t
	}
	if _, has := m["properties"]; has {
		if n.typ == "" {
			n.typ = "object" // properties imply object, config ergonomics
		}
		if n.typ != "object" {
			return nil, fmt.Errorf("%s: properties require type \"object\", got %q", path, n.typ)
		}
	}
	if n.typ == "" {
		return nil, fmt.Errorf("%s: schema node needs a \"type\"", path)
	}
	if d, ok := m["description"].(string); ok {
		n.desc = d
	}
	if e, ok := m["enum"]; ok {
		arr, ok := e.([]any)
		if !ok || len(arr) == 0 {
			return nil, fmt.Errorf("%s: enum must be a non-empty array", path)
		}
		if n.typ == "object" || n.typ == "array" {
			return nil, fmt.Errorf("%s: enum is only supported on scalar schemas", path)
		}
		n.enum = arr
	}
	if v, ok := m["default"]; ok && v != nil {
		if n.typ == "object" {
			return nil, fmt.Errorf("%s: defaults on object schemas are not supported", path)
		}
		n.def = v
	}
	switch n.typ {
	case "object":
		if props, ok := m["properties"].(map[string]any); ok {
			for _, name := range sortedKeys(props) {
				pv, ok := props[name].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s: property %q schema must be an object", path, name)
				}
				sub, err := parseNode(pv, joinPath(path, name))
				if err != nil {
					return nil, err
				}
				n.props[name] = sub
			}
		}
		if req, has := m["required"]; has {
			arr, ok := req.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: required must be an array", path)
			}
			for _, r := range arr {
				rs, ok := r.(string)
				if !ok {
					return nil, fmt.Errorf("%s: required entries must be strings", path)
				}
				if _, exists := n.props[rs]; !exists {
					return nil, fmt.Errorf("%s: required %q is not a declared property", path, rs)
				}
				n.required[rs] = true
			}
		}
	case "array":
		if it, has := m["items"]; has {
			im, ok := it.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: items must be an object", path)
			}
			sub, err := parseNode(im, joinPath(path, "items"))
			if err != nil {
				return nil, err
			}
			n.items = sub
		} else {
			n.items = &schemaNode{typ: "string"}
		}
	}
	return n, nil
}

// normalize validates args against the schema and returns the normalized
// set: loose inputs are coerced (CLI flags and HTTP query arrive as
// strings, MCP JSON numbers as float64), defaults fill absent keys, and
// enum memberships hold. Unknown keys pass through untouched, so transports
// may attach extra data for forwarding tools. All failures are collected
// into one KindInvalidInput error listing every problem.
func (n *schemaNode) normalize(args map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	var problems []string
	for _, name := range sortedKeys(n.props) {
		sub := n.props[name]
		raw, present := args[name]
		if !present || raw == nil {
			if sub.def != nil {
				v, err := sub.coerce(sub.def)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: bad default: %v", name, err))
					continue
				}
				out[name] = v
			} else if n.required[name] {
				problems = append(problems, fmt.Sprintf("%s: missing required parameter", name))
			}
			continue
		}
		v, err := sub.coerce(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if len(sub.enum) > 0 {
			if err := checkEnum(sub, name, v); err != nil {
				problems = append(problems, err.Error())
				continue
			}
		}
		out[name] = v
	}
	if len(problems) > 0 {
		return nil, errs.New(errs.KindInvalidInput, strings.Join(problems, "; "))
	}
	return out, nil
}

// coerce converts a loosely-typed input into the schema type. Conversions
// mirror what the xyz-go decode step does for its compile-time structs, so
// dynamic tools behave identically to defined ones.
func (n *schemaNode) coerce(raw any) (any, error) {
	switch n.typ {
	case "string":
		switch v := raw.(type) {
		case string:
			return v, nil
		case float64:
			if math.Trunc(v) == v {
				return strconv.FormatInt(int64(v), 10), nil
			}
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		case bool:
			return strconv.FormatBool(v), nil
		case json.Number:
			return v.String(), nil
		default:
			return nil, fmt.Errorf("want a string, got %T", raw)
		}
	case "integer":
		switch v := raw.(type) {
		case float64:
			if math.Trunc(v) != v {
				return nil, fmt.Errorf("want an integer, got %v", v)
			}
			return int64(v), nil
		case int64:
			return v, nil
		case int:
			return int64(v), nil
		case json.Number:
			i, err := v.Int64()
			if err != nil {
				return nil, fmt.Errorf("want an integer, got %q", v.String())
			}
			return i, nil
		case string:
			i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("want an integer, got %q", v)
			}
			return i, nil
		default:
			return nil, fmt.Errorf("want an integer, got %T", raw)
		}
	case "number":
		switch v := raw.(type) {
		case float64:
			return v, nil
		case int64:
			return float64(v), nil
		case int:
			return float64(v), nil
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return nil, fmt.Errorf("want a number, got %q", v.String())
			}
			return f, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("want a number, got %q", v)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("want a number, got %T", raw)
		}
	case "boolean":
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("want a boolean, got %q", v)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("want a boolean, got %T", raw)
		}
	case "array":
		switch v := raw.(type) {
		case []any:
			out := make([]any, len(v))
			for i, e := range v {
				c, err := n.items.coerce(e)
				if err != nil {
					return nil, fmt.Errorf("element %d: %v", i, err)
				}
				out[i] = c
			}
			return out, nil
		case []string:
			out := make([]any, len(v))
			for i, e := range v {
				c, err := n.items.coerce(e)
				if err != nil {
					return nil, fmt.Errorf("element %d: %v", i, err)
				}
				out[i] = c
			}
			return out, nil
		case string:
			parts := strings.Split(v, ",")
			out := make([]any, len(parts))
			for i, e := range parts {
				c, err := n.items.coerce(strings.TrimSpace(e))
				if err != nil {
					return nil, fmt.Errorf("element %d: %v", i, err)
				}
				out[i] = c
			}
			return out, nil
		default:
			return nil, fmt.Errorf("want an array, got %T", raw)
		}
	default: // object
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("want an object, got %T", raw)
		}
		return n.normalize(m)
	}
}

// checkEnum coerces the declared enum values to the same shape as v and
// verifies membership, so configs that write numeric enums like [1, 2]
// survive JSON round-tripping into float64.
func checkEnum(n *schemaNode, name string, v any) error {
	coerced := make([]any, 0, len(n.enum))
	for _, e := range n.enum {
		c, err := n.coerce(e)
		if err != nil {
			return fmt.Errorf("%s: bad enum entry %v: %v", name, e, err)
		}
		if c == v {
			return nil
		}
		coerced = append(coerced, c)
	}
	return fmt.Errorf("%s: value %v not in enum %v", name, v, coerced)
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
