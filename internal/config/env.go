package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// LookupFunc resolves an environment variable.
type LookupFunc func(name string) (string, bool)

// ExpandEnv replaces ${VAR} and ${VAR:-default} in s. Unset variables expand
// to the empty string (or the default) and are reported in missing.
func ExpandEnv(s string, lookup LookupFunc) (out string, missing []string) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	out = envRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRef.FindStringSubmatch(m)
		name, def := sub[1], sub[2]
		if v, ok := lookup(name); ok && v != "" {
			return v
		}
		if strings.Contains(m, ":-") {
			return def
		}
		missing = append(missing, name)
		return ""
	})
	return out, missing
}

// ParseDotEnv parses KEY=VALUE lines. It tolerates a UTF-8 BOM, blank lines,
// '#' comments, an optional "export " prefix and single/double quoted values.
func ParseDotEnv(content string) (map[string]string, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	vars := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		vars[key] = unquoteEnvValue(strings.TrimSpace(val))
	}
	return vars, sc.Err()
}

func unquoteEnvValue(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// LoadDotEnv reads path and exports each variable that is not already set in
// the process environment, so real environment variables always win. A
// missing file is not an error. It returns the names that were applied.
func LoadDotEnv(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	vars, err := ParseDotEnv(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var applied []string
	for k, v := range vars {
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return applied, fmt.Errorf("%s: set %s: %w", path, k, err)
		}
		applied = append(applied, k)
	}
	return applied, nil
}
