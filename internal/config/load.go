package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Error names the config file and field that failed validation.
type Error struct {
	File  string
	Field string
	Msg   string
}

func (e *Error) Error() string {
	switch {
	case e.File != "" && e.Field != "":
		return fmt.Sprintf("%s: %s: %s", e.File, e.Field, e.Msg)
	case e.File != "":
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	default:
		return e.Msg
	}
}

// problems accumulates validation errors for one file.
type problems struct {
	file string
	errs []error
}

func (p *problems) add(field, format string, args ...any) {
	p.errs = append(p.errs, &Error{File: p.file, Field: field, Msg: fmt.Sprintf(format, args...)})
}

func (p *problems) err() error { return errors.Join(p.errs...) }

// readYAML reads path, expands ${VAR} in every string scalar and strictly
// decodes into out (unknown fields are errors). Variables that are referenced
// but unset are returned in missing.
func readYAML(path string, out any, strict bool) (missing []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &Error{File: path, Msg: "配置文件不存在 (config file not found)"}
		}
		return nil, &Error{File: path, Msg: fmt.Sprintf("无法读取配置文件 (cannot read config file): %v", err)}
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, &Error{File: path, Msg: fmt.Sprintf("YAML 格式错误 (invalid YAML): %v", err)}
	}
	if root.Kind == 0 {
		return nil, &Error{File: path, Msg: "文件为空 (file is empty)"}
	}
	seen := map[string]bool{}
	expandNode(&root, func(name string) (string, bool) { return os.LookupEnv(name) }, seen)
	for name := range seen {
		missing = append(missing, name)
	}
	sort.Strings(missing)

	expanded, err := yaml.Marshal(&root)
	if err != nil {
		return nil, &Error{File: path, Msg: fmt.Sprintf("YAML 处理失败 (cannot process YAML): %v", err)}
	}
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(strict)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return missing, &Error{File: path, Msg: fmt.Sprintf("字段类型或名称错误 (invalid field): %s", cleanYAMLError(err))}
	}
	return missing, nil
}

// yamlValueRE matches the offending scalar yaml.v3 quotes in type errors
// ("cannot unmarshal !!str `value` into bool"), which may be a secret.
var yamlValueRE = regexp.MustCompile("`[^`]*`")

func cleanYAMLError(err error) string {
	return yamlValueRE.ReplaceAllString(strings.TrimPrefix(err.Error(), "yaml: "), "`***`")
}

func expandNode(n *yaml.Node, lookup LookupFunc, missing map[string]bool) {
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!str" || n.Tag == "") {
		v, miss := ExpandEnv(n.Value, lookup)
		if v != n.Value {
			n.Value = v
			n.Tag = "!!str"
			// Quoted style keeps values like "123" or "true" from being re-typed after expansion.
			n.Style = yaml.DoubleQuotedStyle
		}
		for _, m := range miss {
			missing[m] = true
		}
	}
	for _, c := range n.Content {
		expandNode(c, lookup, missing)
	}
}

// Bundle is the full set of configuration files for one deployment.
type Bundle struct {
	App         *App
	Inspection  *Inspection
	Services    *Services
	Dir         string
	AppPath     string
	InspectPath string
	ServicePath string
	// Warnings lists non-fatal findings such as unset ${VAR} references.
	Warnings []string
}

// LoadBundle loads app.yaml and, from the same directory, inspection.yaml and
// services.yaml, then cross-validates them.
func LoadBundle(appPath string) (*Bundle, error) {
	dir := filepath.Dir(appPath)
	b := &Bundle{
		Dir:         dir,
		AppPath:     appPath,
		InspectPath: filepath.Join(dir, "inspection.yaml"),
		ServicePath: filepath.Join(dir, "services.yaml"),
	}
	var errs []error
	var err error
	if b.App, err = LoadApp(appPath); err != nil {
		errs = append(errs, err)
	}
	if b.Services, err = LoadServices(b.ServicePath); err != nil {
		errs = append(errs, err)
	}
	if b.Inspection, err = LoadInspection(b.InspectPath); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := b.Inspection.ValidateAgainst(b.InspectPath, b.Services, b.App.Labels); err != nil {
		return nil, err
	}
	b.Warnings = append(b.Warnings, b.App.Warnings...)
	return b, nil
}
