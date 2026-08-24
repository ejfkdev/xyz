package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	errs "github.com/ejfkdev/xyz-go/errors"
)

// ExecConfig declares a local command-line adapter. Every call spawns one
// process; Args and Env entries may reference input properties as {name}
// placeholders.
type ExecConfig struct {
	// Program is the executable. Args optionally carry {name} templates.
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
	// Env adds or overrides environment entries; values use the same
	// {name} interpolation. The inherited environment is preserved.
	Env map[string]string `json:"env,omitempty"`
	// Timeout bounds one invocation ("30s" default; the caller's context
	// deadline, when set, still wins).
	Timeout string `json:"timeout,omitempty"`
	// MaxOutput caps returned stdout bytes (default 1 MiB).
	MaxOutput int64 `json:"max_output,omitempty"`
}

const (
	defaultExecTimeout = 30 * time.Second
	defaultMaxOutput   = 1 << 20
)

var placeholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// ExecInvoker binds the config to an Invoker. The result is the trimmed
// stdout text; a non-zero exit makes the call fail with the program's
// stderr as the message, mapped through the xyz error taxonomy (internal,
// or unavailable on timeout / spawn failure).
func ExecInvoker(cfg ExecConfig) Invoker {
	timeout := defaultExecTimeout
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = d
		}
	}
	maxOut := int64(defaultMaxOutput)
	if cfg.MaxOutput > 0 {
		maxOut = cfg.MaxOutput
	}
	return func(ctx context.Context, args map[string]any) (any, error) {
		argv, err := renderArgs(cfg.Args, args)
		if err != nil {
			return nil, errs.New(errs.KindInvalidInput, err.Error())
		}
		runCtx := ctx
		var cancel context.CancelFunc
		if _, has := ctx.Deadline(); !has {
			runCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		cmd := exec.CommandContext(runCtx, cfg.Program, argv...)
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			ev, err := renderTemplate(v, args)
			if err != nil {
				return nil, errs.New(errs.KindInvalidInput, err.Error())
			}
			cmd.Env = append(cmd.Env, k+"="+ev)
		}
		out, err := cmd.Output()
		if err != nil {
			return nil, execFailure(cfg.Program, runCtx, err)
		}
		if int64(len(out)) > maxOut {
			out = out[:maxOut]
		}
		return strings.TrimSpace(string(out)), nil
	}
}

func execFailure(program string, ctx context.Context, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		return errs.WrapMsg(errs.KindUnavailable, err, fmt.Sprintf("command %s timed out", program))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg := strings.TrimSpace(string(ee.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", ee.ExitCode())
		}
		return errs.WrapMsg(errs.KindInternal, err, fmt.Sprintf("command %s: %s", program, msg))
	}
	return errs.Wrap(errs.KindUnavailable, err)
}

// renderArgs instantiates the argv templates:
//
//   - elements without placeholders pass through verbatim;
//   - an element that is exactly {name} with a boolean value becomes
//     "--name" when true and is dropped when false; other types become the
//     value's string form; absent parameters drop the element;
//   - elements containing placeholders replace each occurrence (slices
//     join with ","; booleans render "true"/"false"); if any referenced
//     parameter is absent the whole element is dropped.
//
// Placeholder typos are rejected earlier, by ExecConfig.validate at build
// time, so a missing key at runtime always means "optional, skip".
func renderArgs(tmpls []string, args map[string]any) ([]string, error) {
	var out []string
	for _, t := range tmpls {
		matches := placeholderRe.FindAllStringSubmatch(t, -1)
		if len(matches) == 0 {
			out = append(out, t)
			continue
		}
		if len(matches) == 1 && matches[0][0] == t {
			name := matches[0][1]
			v, present := args[name]
			if !present {
				continue
			}
			if b, isBool := v.(bool); isBool {
				if b {
					out = append(out, "--"+name)
				}
				continue
			}
			out = append(out, renderScalar(v))
			continue
		}
		rendered := t
		missing := false
		for _, m := range matches {
			v, present := args[m[1]]
			if !present {
				missing = true
				break
			}
			rendered = strings.ReplaceAll(rendered, m[0], renderScalar(v))
		}
		if !missing {
			out = append(out, rendered)
		}
	}
	return out, nil
}

// renderTemplate applies the same interpolation to one env value; absence
// of a referenced parameter is an error there, because a half-formed env
// entry would silently change the child's environment.
func renderTemplate(t string, args map[string]any) (string, error) {
	out := t
	for _, m := range placeholderRe.FindAllStringSubmatch(t, -1) {
		v, present := args[m[1]]
		if !present {
			return "", fmt.Errorf("parameter {%s} is required by the template but was not provided", m[1])
		}
		out = strings.ReplaceAll(out, m[0], renderScalar(v))
	}
	return out, nil
}

func renderScalar(v any) string {
	switch x := v.(type) {
	case []string:
		return strings.Join(x, ",")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = renderScalar(e)
		}
		return strings.Join(parts, ",")
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// validate performs the Build-time checks that ExecInvoker's type cannot
// report: program present, timeout parseable, output cap sane, and every
// template placeholder pointing at a declared input property.
func (c *ExecConfig) validate(input map[string]any) error {
	if strings.TrimSpace(c.Program) == "" {
		return fmt.Errorf("exec.program must not be empty")
	}
	if c.Timeout != "" {
		if _, err := time.ParseDuration(c.Timeout); err != nil {
			return fmt.Errorf("exec.timeout %q: %w", c.Timeout, err)
		}
	}
	if c.MaxOutput < 0 {
		return fmt.Errorf("exec.max_output must not be negative")
	}
	if input == nil {
		return nil // 无 schema 声明的透传型工具：占位符不设静态约束
	}
	node, err := parseSchema(input)
	if err != nil {
		return err
	}
	for _, t := range c.Args {
		if err := checkPlaceholders(t, node); err != nil {
			return fmt.Errorf("exec.args: %w", err)
		}
	}
	for _, t := range c.Env {
		if err := checkPlaceholders(t, node); err != nil {
			return fmt.Errorf("exec.env: %w", err)
		}
	}
	return nil
}

func checkPlaceholders(t string, node *schemaNode) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(t, -1) {
		if _, ok := node.props[m[1]]; !ok {
			return fmt.Errorf("placeholder {%s} is not a declared input property", m[1])
		}
	}
	return nil
}
