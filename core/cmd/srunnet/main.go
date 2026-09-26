// Command srunnet is the smart-srun 2.0 client.
//
// The CLI and LuCI share the daemon's configuration and authentication workers.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/matthewlu070111/smart-srun/core/internal/cli"
)

func main() {
	// SIGTERM is what procd sends to stop the service, and cancelling the
	// context is how that reaches every loop inside it. Nothing here forks or
	// writes a pidfile: a process that backgrounded itself is a process procd
	// could not supervise.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		return cli.RunStatus(ctx, nil, stdout, stderr)
	}

	switch args[0] {
	case "version", "--version", "-V":
		fmt.Fprintln(stdout, cli.VersionString())
		return cli.ExitOK
	case "help", "man", "--help", "-h":
		usage(stdout)
		return cli.ExitOK
	case "config":
		if cli.OnlineConfig(args[1:]) {
			return cli.RunOnline(ctx, args, os.Stdin, stdout, stderr)
		}
		return cli.RunConfig(args[1:], stdout, stderr)
	case "login", "logout", "relogin", "switch", "enable", "disable", "detect", "presets", "schools", "log":
		return cli.RunOnline(ctx, args, os.Stdin, stdout, stderr)
	case "daemon":
		return cli.RunDaemon(ctx, args[1:], stdout, stderr)
	case "update":
		return cli.RunUpdate(ctx, args[1:], stdout, stderr)
	case "_update-worker":
		return cli.RunUpdateWorker(ctx, args[1:], stdout, stderr)
	case "_update-intent":
		return cli.RunUpdateIntent(args[1:], stderr)
	case "_update-worker-active":
		return cli.UpdateWorkerActive()
	case "service":
		return cli.RunService(ctx, args[1:], stdout, stderr)
	case "status":
		return cli.RunStatus(ctx, args[1:], stdout, stderr)
	}

	if cli.IsCoreCommand(args[0]) {
		fmt.Fprintf(stderr,
			"命令 %q 本次构建尚未包含，运行 srunnet help 查看可用命令。\n",
			args[0])
		return cli.ExitUnsupported
	}
	fmt.Fprintf(stderr, "未知命令 %q，运行 srunnet help 查看可用命令。\n", args[0])
	return cli.ExitInvalidInput
}

func usage(out *os.File) { cli.WriteHelp(out) }
