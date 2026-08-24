package bridge

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	errs "github.com/ejfkdev/xyz-go/errors"
)

func TestRenderArgs(t *testing.T) {
	cases := []struct {
		name  string
		tmpls []string
		args  map[string]any
		want  []string
	}{
		{"verbatim", []string{"+short"}, nil, []string{"+short"}},
		{"plain value", []string{"{host}"}, map[string]any{"host": "example.com"}, []string{"example.com"}},
		{"bool whole true", []string{"{long}"}, map[string]any{"long": true}, []string{"--long"}},
		{"bool whole false", []string{"{long}"}, map[string]any{"long": false}, nil},
		{"absent drops whole", []string{"{opt}"}, map[string]any{}, nil},
		{"mixed replace", []string{"--type={qtype}"}, map[string]any{"qtype": "A"}, []string{"--type=A"}},
		{"mixed absent drops", []string{"--type={qtype}"}, map[string]any{}, nil},
		{"slice joins", []string{"--tags={tags}"}, map[string]any{"tags": []string{"a", "b"}}, []string{"--tags=a,b"}},
		{"bool inline renders", []string{"--quiet={q}"}, map[string]any{"q": false}, []string{"--quiet=false"}},
		{"multi placeholder", []string{"{a}-{b}"}, map[string]any{"a": int64(1), "b": "x"}, []string{"1-x"}},
		{"repeat placeholder", []string{"{x}:{x}"}, map[string]any{"x": "v"}, []string{"v:v"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := renderArgs(c.tmpls, c.args)
			if err != nil {
				t.Fatalf("renderArgs: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestRenderTemplateErrorsWhenAbsent(t *testing.T) {
	if _, err := renderTemplate("token={tok}", map[string]any{}); err == nil {
		t.Error("absent placeholder in env accepted, want error")
	}
}

func TestExecInvokerEcho(t *testing.T) {
	inv := ExecInvoker(ExecConfig{Program: "/bin/echo", Args: []string{"{msg}"}})
	out, err := inv(context.Background(), map[string]any{"msg": "hi there"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out != "hi there" {
		t.Errorf("out = %q", out)
	}
}

func TestExecInvokerExitCode(t *testing.T) {
	inv := ExecInvoker(ExecConfig{
		Program: "/bin/sh",
		Args:    []string{"-c", "echo oops >&2; exit 3"},
	})
	_, err := inv(context.Background(), nil)
	if err == nil {
		t.Fatal("non-zero exit accepted")
	}
	if errs.Classify(err) != errs.KindInternal {
		t.Errorf("classify = %q, want internal", errs.Classify(err))
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("message should carry stderr: %v", err)
	}
}

func TestExecInvokerTimeout(t *testing.T) {
	inv := ExecInvoker(ExecConfig{
		Program: "/bin/sh",
		Args:    []string{"-c", "sleep 5"},
		Timeout: "100ms",
	})
	start := time.Now()
	_, err := inv(context.Background(), nil)
	if err == nil {
		t.Fatal("timeout not enforced")
	}
	if errs.Classify(err) != errs.KindUnavailable {
		t.Errorf("classify = %q, want unavailable", errs.Classify(err))
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout took %v, want ~100ms", time.Since(start))
	}
}

func TestExecInvokerEnvTemplate(t *testing.T) {
	inv := ExecInvoker(ExecConfig{
		Program: "/bin/sh",
		Args:    []string{"-c", "printf '%s' \"$XYZ_TOKEN\""},
		Env:     map[string]string{"XYZ_TOKEN": "{token}"},
	})
	out, err := inv(context.Background(), map[string]any{"token": "s3cret"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out != "s3cret" {
		t.Errorf("out = %q, want env-interpolated token", out)
	}
}

func TestExecInvokerMaxOutput(t *testing.T) {
	// dd 生成确定性的 250 个 'x'（无空白），截断不改变字节内容。
	inv := ExecInvoker(ExecConfig{
		Program:   "/bin/sh",
		Args:      []string{"-c", "dd if=/dev/zero bs=250 count=1 | tr '\\0' x"},
		MaxOutput: 100,
	})
	out, err := inv(context.Background(), nil)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if s := out.(string); len(s) != 100 {
		t.Errorf("output len = %d, want capped at 100", len(s))
	}
}

func TestExecConfigValidate(t *testing.T) {
	input := map[string]any{
		"type":       "object",
		"properties": map[string]any{"host": map[string]any{"type": "string"}},
	}
	if err := (&ExecConfig{}).validate(input); err == nil {
		t.Error("empty program accepted")
	}
	if err := (&ExecConfig{Program: "x", Timeout: "soon"}).validate(input); err == nil {
		t.Error("bad timeout accepted")
	}
	if err := (&ExecConfig{Program: "x", Args: []string{"{nope}"}}).validate(input); err == nil {
		t.Error("undeclared placeholder accepted")
	}
	if err := (&ExecConfig{Program: "x", Args: []string{"{host}"}}).validate(input); err != nil {
		t.Errorf("declared placeholder rejected: %v", err)
	}
	if err := (&ExecConfig{Program: "x", Args: []string{"{anything}"}}).validate(nil); err != nil {
		t.Errorf("no-schema tool should skip static checks: %v", err)
	}
}
