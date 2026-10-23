package main

import (
	"fmt"
	"io"
)

// The commands below validate their flags and configuration, then report that
// the implementation is not available yet. They are replaced as features land.

func notImplemented(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "命令 %q 尚未实现 (not implemented yet)\n", name)
	return 1
}

func runServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if _, err := loadConfig(*cfgPath, stderr); err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	return notImplemented("serve", stderr)
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("inspect", stderr)
	cfgPath := fs.String("config", defaultConfigPath, "app.yaml 路径 (path to app.yaml)")
	fs.String("out", "", "报告输出目录, 默认取配置 report.out_dir (report output dir)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	if _, err := loadConfig(*cfgPath, stderr); err != nil {
		fmt.Fprintf(stderr, "配置错误 (config error):\n%v\n", err)
		return 1
	}
	return notImplemented("inspect", stderr)
}

func runMockNotify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("mock-notify", stderr)
	fs.String("listen", "127.0.0.1:18091", "监听地址 (listen address)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	return notImplemented("mock-notify", stderr)
}
