package config

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Categories reported for YAML failures. yaml.v3 messages routinely quote
// source text (scalars, anchor names, mapping keys), which may be a secret,
// so nothing from the library message is ever copied into the output.
const (
	catSyntax  = "语法错误"
	catType    = "类型错误"
	catUnknown = "未知字段"
	catAnchor  = "未定义锚点"
)

var (
	yamlLineRE         = regexp.MustCompile(`^(?:yaml: )?line (\d+):`)
	yamlTypeLineRE     = regexp.MustCompile(`^line (\d+): `)
	yamlUnknownFldRE   = regexp.MustCompile(`^line \d+: field .+ not found in type `)
	yamlDuplicateKeyRE = regexp.MustCompile(`^line \d+: mapping key .+ already defined at line `)
	yamlUnknownAnchor  = "yaml: unknown anchor '"
)

// yamlLoc is where a node of the decoded document came from in the user's file.
type yamlLoc struct {
	line   int
	path   string
	parent string
}

// yamlLocator translates a line reported by the decode stage into the line of
// the original file and the field path at that position.
type yamlLocator map[int]yamlLoc

// buildLocator pairs the user's parsed document with the re-serialised
// document that is actually decoded. Both have the same shape, but expansion
// and re-marshalling move lines around.
func buildLocator(orig *yaml.Node, expanded []byte) yamlLocator {
	var exp yaml.Node
	if err := yaml.Unmarshal(expanded, &exp); err != nil {
		return nil
	}
	m := yamlLocator{}
	walkPair(orig, &exp, "", "", m)
	return m
}

func walkPair(o, e *yaml.Node, path, parent string, m yamlLocator) {
	if o == nil || e == nil || o.Kind != e.Kind {
		return
	}
	if e.Line > 0 && o.Kind != yaml.DocumentNode {
		m[e.Line] = yamlLoc{line: o.Line, path: path, parent: parent}
	}
	switch o.Kind {
	case yaml.DocumentNode:
		if len(o.Content) == len(e.Content) {
			for i := range o.Content {
				walkPair(o.Content[i], e.Content[i], path, parent, m)
			}
		}
	case yaml.MappingNode:
		if len(o.Content) != len(e.Content) {
			return
		}
		for i := 0; i+1 < len(o.Content); i += 2 {
			k := joinPath(path, o.Content[i].Value)
			if ek := e.Content[i]; ek.Line > 0 {
				m[ek.Line] = yamlLoc{line: o.Content[i].Line, path: k, parent: path}
			}
			walkPair(o.Content[i+1], e.Content[i+1], k, path, m)
		}
	case yaml.SequenceNode:
		if len(o.Content) != len(e.Content) {
			return
		}
		for i := range o.Content {
			walkPair(o.Content[i], e.Content[i], fmt.Sprintf("%s[%d]", path, i), path, m)
		}
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// yamlFailure converts any error produced by yaml.v3 for file path into one
// *Error per problem, carrying only the file, a line, a field path (when
// known) and a category. It is the single place YAML errors leave the loader.
// data is the original file content and loc may be nil.
func yamlFailure(path string, data []byte, err error, loc yamlLocator) error {
	var errs []error
	add := func(cat string, line int, field, parent string) {
		msg := "YAML " + cat
		if cat == catUnknown {
			// The unknown key itself is user text, so only its parent is named.
			field = parent
			if parent != "" {
				msg += " (位于该节点之下)"
			}
		}
		if line > 0 {
			msg += " (第 " + strconv.Itoa(line) + " 行)"
		}
		errs = append(errs, &Error{File: path, Field: field, Msg: msg})
	}
	resolve := func(line int) yamlLoc {
		if loc == nil {
			return yamlLoc{}
		}
		return loc[line]
	}

	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, s := range te.Errors {
			line := 0
			if m := yamlTypeLineRE.FindStringSubmatch(s); m != nil {
				line, _ = strconv.Atoi(m[1])
			}
			at := resolve(line)
			switch {
			case yamlUnknownFldRE.MatchString(s):
				add(catUnknown, at.line, at.path, at.parent)
			case yamlDuplicateKeyRE.MatchString(s):
				add(catSyntax, at.line, "", "")
			default:
				add(catType, at.line, at.path, "")
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
	}

	msg := err.Error()
	if strings.HasPrefix(msg, yamlUnknownAnchor) {
		name := strings.TrimSuffix(strings.TrimPrefix(msg, yamlUnknownAnchor), "' referenced")
		add(catAnchor, anchorLine(data, name), "", "")
		return errors.Join(errs...)
	}
	line := 0
	if m := yamlLineRE.FindStringSubmatch(msg); m != nil {
		line, _ = strconv.Atoi(m[1])
		if at, ok := loc[line]; ok {
			line = at.line
		}
	}
	add(catSyntax, line, "", "")
	return errors.Join(errs...)
}

// anchorLine finds the first alias "*name" in data; yaml.v3 does not report a
// line for unknown anchors. The name is only used for searching, never printed.
func anchorLine(data []byte, name string) int {
	if name == "" {
		return 0
	}
	needle := []byte("*" + name)
	for off := 0; off < len(data); {
		i := bytes.Index(data[off:], needle)
		if i < 0 {
			return 0
		}
		end := off + i + len(needle)
		if end == len(data) || strings.IndexByte(" \t\r\n,]}", data[end]) >= 0 {
			return bytes.Count(data[:off+i], []byte("\n")) + 1
		}
		off = end
	}
	return 0
}
