package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"

	"yunwei/internal/notify"
)

// runMockNotify serves the mock webhook receiver until interrupted.
func runMockNotify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("mock-notify", stderr)
	listen := fs.String("listen", "127.0.0.1:18091", "监听地址 (listen address)")
	if code, done := parseFlags(fs, args); done {
		return code
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "无法监听 (cannot listen): %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "mock-notify listening on %s\n", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := notify.NewMockReceiver().Serve(ctx, ln); err != nil {
		fmt.Fprintf(stderr, "mock-notify 异常退出: %v\n", err)
		return 1
	}
	return 0
}
