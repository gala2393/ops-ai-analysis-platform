// Command yunwei is the ops AI analysis platform: web UI, inspection CLI and
// the mock services used for development and acceptance testing.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const usageText = `yunwei - 运维 AI 分析平台 (ops AI analysis platform)

用法 (usage):
  yunwei <command> [flags]

命令 (commands):
  serve        启动 Web UI / API / 定时巡检 / Alertmanager webhook   (-config)
  inspect      执行一次巡检并输出 Markdown + HTML 报告              (-config -out)
  mock-prom    启动 Mock Prometheus                                (-scenario -listen -expected-out)
  mock-notify  启动 Mock 通知接收端 (飞书/钉钉/企业微信)            (-listen)
  check-prom   检查 Prometheus 连通性、版本与 targets 健康          (-config)
  check-config 校验配置文件并打印脱敏后的主配置                     (-config)
  help         显示本帮助

示例 (example):
  yunwei check-config -config config/app.yaml
`

type command struct {
	name string
	run  func(args []string, stdout, stderr io.Writer) int
}

var commands = []command{
	{"serve", runServe},
	{"inspect", runInspect},
	{"mock-prom", runMockProm},
	{"mock-notify", runMockNotify},
	{"check-prom", runCheckProm},
	{"check-config", runCheckConfig},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches to a subcommand and returns the process exit code:
// 0 success, 1 runtime or configuration error, 2 usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "未知命令 (unknown command): %q\n\n%s", args[0], usageText)
	return 2
}

// newFlagSet returns a FlagSet that reports errors to stderr instead of exiting.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("yunwei "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseFlags returns (exitCode, done); when done is true the caller must return exitCode.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0, true
		}
		return 2, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "多余的参数 (unexpected arguments): %v\n", fs.Args())
		return 2, true
	}
	return 0, false
}
