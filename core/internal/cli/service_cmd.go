package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
)

// RunDaemon runs the service in the foreground until the context is done.
//
// procd supervises it; nothing here forks, writes a pidfile or daemonises. A
// process that backgrounded itself would be a process procd could not
// supervise, respawn or stop.
func RunDaemon(ctx context.Context, args []string, stdout, stderr *os.File) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "用法：srunnet daemon")
		return ExitInvalidInput
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	capabilities := openwrt.Detect(probeCtx, openwrt.Runner{})
	cancel()
	err := daemon.Run(ctx, daemon.Options{
		Paths:        daemon.DefaultPaths(),
		Version:      Version,
		Capabilities: capabilities,
		// Faults with nobody to return them to go to stderr, which procd
		// captures. The structured event log replaces this in M11.
		OnError: func(err error) { fmt.Fprintf(stderr, "srunnet: %v\n", err) },
	})
	if err != nil {
		fmt.Fprintf(stderr, "srunnet: %v\n", err)
		return ExitCodeFor(err)
	}
	fmt.Fprintln(stdout, "认证服务已停止")
	return ExitOK
}

// defaultLifecycle is what the commands below drive on a device.
//
// The paths and the init script are fixed, not taken from a caller: spec 02
// requires the lifecycle helper to be one command that starts one service, and
// the page invoking it passes no arguments at all. The internal functions take
// a Lifecycle so the exit codes and messages can be tested against a temporary
// directory -- that is a narrower seam than a path parameter, and nothing on
// the wire reaches it.
func defaultLifecycle() daemon.Lifecycle {
	return daemon.Lifecycle{Paths: daemon.DefaultPaths(), Runner: openwrt.Runner{}}
}

// RunService is the fixed lifecycle helper spec 02 defines.
//
// Three sub-commands, no arguments beyond them. It exists so that an explicit
// user action on a page whose service is stopped can start it and then submit
// its own request -- not so that anything can be asked to run an arbitrary
// service.
func RunService(ctx context.Context, args []string, stdout, stderr *os.File) int {
	return runService(ctx, defaultLifecycle(), args, stdout, stderr)
}

func runService(ctx context.Context, lifecycle daemon.Lifecycle, args []string,
	stdout, stderr *os.File) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法：srunnet service ensure-running|stop|status")
		return ExitInvalidInput
	}

	switch args[0] {
	case "ensure-running":
		if err := lifecycle.EnsureRunning(ctx); err != nil {
			fmt.Fprintf(stderr, "srunnet: %v\n", err)
			return ExitCodeFor(err)
		}
		fmt.Fprintln(stdout, "认证服务已在运行")
		return ExitOK

	case "stop":
		report, err := lifecycle.Stop(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "srunnet: %v\n", err)
			return ExitCodeFor(err)
		}
		switch {
		case report.AlreadyStopped:
			fmt.Fprintln(stdout, "认证服务本来就没有运行")
		case report.CancelledActions > 0:
			fmt.Fprintf(stdout, "已取消 %d 个进行中的动作，认证服务已停止\n",
				report.CancelledActions)
		default:
			fmt.Fprintln(stdout, "认证服务已停止")
		}
		// Worth saying out loud, because the button that does this is next to
		// the automatic-authentication switch and users reasonably wonder.
		fmt.Fprintln(stdout, "自动认证开关未被改动。")
		return ExitOK

	case "status":
		return runServiceStatus(ctx, lifecycle, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "未知子命令 %q，可用：ensure-running、stop、status\n", args[0])
		return ExitInvalidInput
	}
}

// RunStatus is the default command. It reads and never starts anything.
func RunStatus(ctx context.Context, args []string, stdout, stderr *os.File) int {
	return runStatus(ctx, defaultLifecycle(), args, stdout, stderr)
}

func runStatus(ctx context.Context, lifecycle daemon.Lifecycle, args []string,
	stdout, stderr *os.File) int {
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			fmt.Fprintln(stderr, "用法：srunnet status [--json]")
			return ExitInvalidInput
		}
		asJSON = true
	}

	snapshot, err := lifecycle.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "srunnet: %v\n", err)
		return ExitCodeFor(err)
	}
	if asJSON {
		return writeJSON(stdout, stderr, snapshot)
	}
	printStatus(stdout, snapshot)
	// A stopped service is a fact, not a failure of the status command: a
	// script that polls should not have to treat "it is off" as an error.
	return ExitOK
}

func runServiceStatus(ctx context.Context, lifecycle daemon.Lifecycle,
	stdout, stderr *os.File) int {
	snapshot, err := lifecycle.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "srunnet: %v\n", err)
		return ExitCodeFor(err)
	}
	if snapshot.Service != daemon.ServiceRunning {
		fmt.Fprintln(stdout, "认证服务：已停止")
		return ExitServiceStopped
	}
	fmt.Fprintf(stdout, "认证服务：运行中（进程 %d）\n", snapshot.PID)
	return ExitOK
}

func printStatus(stdout *os.File, snapshot daemon.Snapshot) {
	if snapshot.Service == daemon.ServiceRunning {
		fmt.Fprintf(stdout, "认证服务：运行中（进程 %d）\n", snapshot.PID)
	} else {
		fmt.Fprintln(stdout, "认证服务：已停止")
	}
	fmt.Fprintf(stdout, "自动认证：%s\n", onOff(snapshot.Enabled))
	fmt.Fprintf(stdout, "配置版本：%d\n", snapshot.ConfigRevision)
	for _, id := range snapshot.ManualPausedAccounts {
		fmt.Fprintf(stdout, "账号 %s：已手动暂停自动认证，可手动登录恢复\n", id)
	}

	if len(snapshot.Accounts) == 0 {
		fmt.Fprintln(stdout, "账号：尚无观测结果")
	}
	for _, account := range snapshot.Accounts {
		fmt.Fprintf(stdout, "账号 %s：链路 %s，认证 %s，连通 %s\n",
			account.AccountID, account.Link, account.Auth, account.Connectivity)
		if account.Note != nil {
			fmt.Fprintf(stdout, "  上次动作：%s\n", account.Note.Message)
		}
	}
	for _, action := range snapshot.Actions {
		fmt.Fprintf(stdout, "动作 %s（%s）：%s%s\n",
			action.ID, action.Kind, action.State, phaseSuffix(action))
	}
}

func phaseSuffix(action daemon.ActionView) string {
	if action.Message != "" {
		return " —— " + action.Message
	}
	if action.Phase != "" {
		return "（" + action.Phase + "）"
	}
	return ""
}

func onOff(value bool) string {
	if value {
		return "开"
	}
	return "关"
}
