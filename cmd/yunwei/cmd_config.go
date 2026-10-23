package main

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"yunwei/internal/config"
)

const defaultConfigPath = "config/app.yaml"

// loadConfig loads ./.env (real environment variables win) and then the
// configuration bundle next to appPath. Warnings go to stderr.
func loadConfig(appPath string, stderr io.Writer) (*config.Bundle, error) {
	if _, err := config.LoadDotEnv(".env"); err != nil {
		return nil, err
	}
	b, err := config.LoadBundle(appPath)
	if err != nil {
		return nil, err
	}
	for _, w := range b.Warnings {
		fmt.Fprintf(stderr, "警告 (warning): %s\n", w)
	}
	return b, nil
}

// runCheckConfig validates every config file and prints the redacted app config.
func runCheckConfig(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("check-config", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	b, err := loadConfig(*cfgPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	red := b.App.Redacted()
	out, err := yaml.Marshal(&red)
	if err != nil {
		fmt.Fprintf(stderr, "无法输出配置: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "配置校验通过 (config OK): %d 个巡检项, %d 条服务规则, %d 条覆盖规则\n",
		len(b.Inspection.Items), len(b.Services.Services), len(b.Inspection.Overrides))
	fmt.Fprintf(stdout, "# %s (脱敏 redacted)\n%s", b.AppPath, out)
	return 0
}
