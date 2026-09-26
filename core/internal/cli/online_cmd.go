package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type onlineClient struct {
	call   func(context.Context, string, any) (json.RawMessage, error)
	ensure func(context.Context) error
}

// OnlineConfig distinguishes socket commands from the offline schema/validator.
func OnlineConfig(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "show", "get", "set", "account", "hotspot", "export", "import":
		return true
	}
	return false
}

// RunOnline shares the daemon's use cases. Credentials are accepted through
// bounded JSON stdin, never through command-line arguments or a shell command.
func RunOnline(ctx context.Context, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	client := onlineClient{call: (control.Client{}).Call, ensure: defaultLifecycle().EnsureRunning}
	return runOnline(ctx, client, args, stdin, stdout, stderr)
}

func runOnline(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	if len(args) == 0 {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "需要命令"))
	}
	switch args[0] {
	case "detect":
		return onlineDetect(ctx, client, args[1:], stdin, stdout, stderr)
	case "schools":
		return onlineSchools(args[1:], stdout, stderr)
	case "presets":
		return onlinePresets(ctx, client, args, stdout, stderr)
	case "log":
		return onlineLog(ctx, client, args[1:], stdout, stderr)
	case "config":
		return onlineConfig(ctx, client, args[1:], stdin, stdout, stderr)
	case "enable", "disable":
		if len(args) != 1 {
			return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：srunnet enable|disable"))
		}
		if err := client.ensure(ctx); err != nil {
			return onlineError(stderr, err)
		}
		cfg, err := readOnlineConfig(ctx, client)
		if err != nil {
			return onlineError(stderr, err)
		}
		settings, _ := json.Marshal(map[string]bool{"enabled": args[0] == "enable"})
		return printCall(ctx, client, "config.apply", daemon.ConfigApplyParams{ExpectedRevision: &cfg.Revision, Settings: settings}, stdout, stderr)
	case "login", "logout", "relogin", "switch":
		return onlineAction(ctx, client, args, stdout, stderr)
	default:
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "未知在线命令"))
	}
}

func onlineConfig(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	if len(args) > 0 && (args[0] == "export" || args[0] == "import") {
		return onlineBackup(ctx, client, args, stdin, stdout, stderr)
	}
	explicit := len(args) > 0 && args[len(args)-1] == "--interactive"
	if explicit {
		return runInteractiveConfig(ctx, client, args[:len(args)-1], stdin, stdout, stderr)
	}
	if len(args) >= 2 && (args[1] == "add" || args[1] == "edit") {
		if file, ok := stdin.(*os.File); ok && isTerminal(file) {
			return runInteractiveConfig(ctx, client, args, stdin, stdout, stderr)
		}
	}
	badUsage := func() int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument,
			"用法：config show|get [字段路径]|set；config account|hotspot list|get ID|add|edit|rm|default（写操作从标准输入读取含 expected_revision 的 JSON）"))
	}
	if len(args) == 0 {
		return badUsage()
	}
	if (args[0] == "show" && len(args) == 1) || (args[0] == "get" && len(args) <= 2) {
		raw, err := client.call(ctx, "config.get", nil)
		if err != nil {
			return onlineError(stderr, err)
		}
		if len(args) == 1 {
			return writeJSON(stdout, stderr, raw)
		}
		for _, part := range strings.Split(args[1], ".") {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil || fields[part] == nil {
				return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "字段路径不存在"))
			}
			raw = fields[part]
		}
		return writeJSON(stdout, stderr, raw)
	}
	method := ""
	var target any
	switch {
	case len(args) == 1 && args[0] == "set":
		method, target = "config.apply", &daemon.ConfigApplyParams{}
	case len(args) >= 2 && (args[0] == "account" || args[0] == "hotspot"):
		family := "campus"
		if args[0] == "hotspot" {
			family = "hotspot"
		}
		if args[1] == "list" && len(args) == 2 {
			return printCall(ctx, client, family+".get", nil, stdout, stderr)
		}
		if args[1] == "get" && len(args) == 3 {
			return printCall(ctx, client, family+".get", daemon.DetailParams{ID: args[2]}, stdout, stderr)
		}
		if len(args) != 2 {
			return badUsage()
		}
		switch args[1] {
		case "add", "edit":
			method = family + ".upsert"
			if family == "campus" {
				target = &daemon.CampusUpsertParams{}
			} else {
				target = &daemon.HotspotUpsertParams{}
			}
		case "rm", "default":
			method = family + ".remove"
			if args[1] == "default" {
				method = family + ".set_default"
			}
			target = &daemon.ConfigIDParams{}
		default:
			return badUsage()
		}
	default:
		return badUsage()
	}
	// Other operations require JSON; a terminal must not wait invisibly for it.
	if file, ok := stdin.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "请通过管道或重定向提供 JSON 配置"))
		}
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, config.MaxConfigBytes+1))
	if err != nil {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "无法读取 JSON 输入"))
	}
	if err := config.DecodePatch(raw, target, "account.login.double_stack"); err != nil {
		return onlineError(stderr, err)
	}
	var revision *uint64
	var id string
	switch params := target.(type) {
	case *daemon.ConfigApplyParams:
		revision = params.ExpectedRevision
		var settings config.Settings
		if err := config.DecodePatch(params.Settings, &settings); err != nil {
			return onlineError(stderr, err)
		}
	case *daemon.CampusUpsertParams:
		revision = params.ExpectedRevision
		if params.Account == nil {
			return badUsage()
		}
		id = params.Account.ID
	case *daemon.HotspotUpsertParams:
		revision = params.ExpectedRevision
		if params.Profile == nil {
			return badUsage()
		}
		id = params.Profile.ID
	case *daemon.ConfigIDParams:
		revision, id = params.ExpectedRevision, params.ID
		if id == "" {
			return badUsage()
		}
	}
	if revision == nil {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "JSON 需要 expected_revision；先运行 config show 查看 revision"))
	}
	if len(args) == 2 && (args[1] == "add" && id != "" || args[1] == "edit" && id == "") {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "add 不接受 id；edit 必须指定已有 id"))
	}
	if err := client.ensure(ctx); err != nil {
		return onlineError(stderr, err)
	}
	// Preserve the original bytes: a decode/re-encode must not lose the explicit
	// null that resets an account's double_stack override.
	return printCall(ctx, client, method, json.RawMessage(raw), stdout, stderr)
}

func readOnlineConfig(ctx context.Context, client onlineClient) (domain.Config, error) {
	var cfg domain.Config
	raw, err := client.call(ctx, "config.get", nil)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, domain.Errorf(domain.CodeProtocolInvalid, "服务返回的配置格式无效")
	}
	return cfg, nil
}

func printCall(ctx context.Context, client onlineClient, method string, params any, stdout, stderr *os.File) int {
	raw, err := client.call(ctx, method, params)
	if err != nil {
		return onlineError(stderr, err)
	}
	return writeJSON(stdout, stderr, raw)
}

func onlineError(stderr *os.File, err error) int {
	fmt.Fprintf(stderr, "srunnet: %v\n", err)
	return ExitCodeFor(err)
}

func onlineAction(ctx context.Context, client onlineClient, args []string, stdout, stderr *os.File) int {
	usage := "用法：srunnet login|logout|relogin [账号ID] 或 switch campus|hotspot [ID] [--json] [--no-wait] [--ignore-quiet]"
	kinds := map[string]application.Kind{"login": application.KindLogin, "logout": application.KindLogout, "relogin": application.KindRelogin}
	kind := kinds[args[0]]
	remaining := args[1:]
	if args[0] == "switch" {
		if len(remaining) == 0 || (remaining[0] != "campus" && remaining[0] != "hotspot") {
			return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "%s", usage))
		}
		kind = application.KindSwitchCampus
		if remaining[0] == "hotspot" {
			kind = application.KindSwitchHotspot
		}
		remaining = remaining[1:]
	}
	asJSON, noWait, ignoreQuiet := false, false, false
	id := ""
	for _, arg := range remaining {
		switch arg {
		case "--json":
			asJSON = true
		case "--no-wait":
			noWait = true
		case "--ignore-quiet":
			ignoreQuiet = true
		default:
			if strings.HasPrefix(arg, "-") || id != "" {
				return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "%s", usage))
			}
			id = arg
		}
	}
	ctx, cancel := context.WithTimeout(ctx, application.ActionBudget+application.QueueWait+5*time.Second)
	defer cancel()
	if err := client.ensure(ctx); err != nil {
		return onlineError(stderr, err)
	}
	cfg, err := readOnlineConfig(ctx, client)
	if err != nil {
		return onlineError(stderr, err)
	}
	if id == "" {
		id = cfg.Selection.ActiveCampusID
		if kind == application.KindSwitchCampus {
			id = cfg.Selection.DefaultCampusID
		} else if kind == application.KindSwitchHotspot {
			id = cfg.Selection.DefaultHotspotID
		}
	}
	if id == "" {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidConfig, "尚未配置所需账号或热点，请先添加"))
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return onlineError(stderr, err)
	}
	params := daemon.SubmitParams{
		Kind: string(kind), AccountID: id, IgnoreQuiet: ignoreQuiet,
		IdempotencyKey: "cli-" + hex.EncodeToString(random[:]), ExpectedRevision: &cfg.Revision,
	}
	if kind == application.KindSwitchHotspot {
		params.AccountID, params.HotspotID = "", id
	}
	raw, err := client.call(ctx, "action.submit", params)
	if err != nil {
		return onlineError(stderr, err)
	}
	var receipt daemon.SubmitResult
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.ActionID == "" {
		return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "服务未返回有效的动作编号"))
	}
	if noWait {
		if asJSON {
			return writeJSON(stdout, stderr, receipt)
		}
		fmt.Fprintf(stdout, "动作 %s 已提交，尚未确认完成；运行 srunnet status --json 查看结果。\n", receipt.ActionID)
		return ExitOK
	}
	fmt.Fprintf(stderr, "动作 %s 已提交，等待执行结果。\n", receipt.ActionID)
	view, err := waitAction(ctx, client, receipt.ActionID, stderr)
	if err != nil {
		return onlineError(stderr, err)
	}
	if asJSON {
		if code := writeJSON(stdout, stderr, view); code != ExitOK {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "%s：%s（%s）\n", view.ID, view.Message, view.State)
	}
	if application.State(view.State) == application.StateSucceeded {
		return ExitOK
	}
	if view.Code != "" {
		return ExitCodeFor(domain.Errorf(view.Code, "%s", view.Message))
	}
	if application.State(view.State) == application.StateCancelled {
		return ExitCancelled
	}
	return ExitActionFailed
}

func waitAction(ctx context.Context, client onlineClient, id string, stderr *os.File) (daemon.ActionView, error) {
	view, err := waitTask(ctx, client, id, stderr)
	return view.ActionView, err
}

type taskView struct {
	daemon.ActionView
	Result json.RawMessage `json:"result,omitempty"`
}

func waitTask(ctx context.Context, client onlineClient, id string, stderr *os.File) (taskView, error) {
	phase := ""
	for {
		raw, err := client.call(ctx, "action.get", daemon.ActionParams{ActionID: id})
		if ctx.Err() != nil {
			// Ctrl-C cancels this action, not the service or other users' work.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, cancelErr := client.call(cleanup, "action.cancel", daemon.ActionParams{ActionID: id})
			stop()
			if cancelErr != nil {
				fmt.Fprintf(stderr, "未能确认动作 %s 已取消，请查询状态。\n", id)
			}
			code := domain.CodeCancelled
			if ctx.Err() == context.DeadlineExceeded {
				code = domain.CodeDeadlineExceeded
			}
			return taskView{}, domain.Errorf(code, "等待动作结束已中止")
		}
		if err != nil {
			return taskView{}, err
		}
		var view taskView
		if err := json.Unmarshal(raw, &view); err != nil || view.ID != id {
			return view, domain.Errorf(domain.CodeProtocolInvalid, "服务返回的动作格式无效")
		}
		if application.State(view.State).Terminal() {
			return view, nil
		}
		if view.State != "queued" && view.State != "running" {
			return view, domain.Errorf(domain.CodeProtocolInvalid, "服务返回了未知动作状态")
		}
		if view.Phase != phase {
			phase = view.Phase
			labels := map[string]string{"waiting_link": "等待线路", "challenge": "获取认证挑战", "login": "正在认证", "verify": "验证连接", "logout": "正在退出", "switch": "切换连接", "fetch": "正在读取",
				"prepare": "准备连接", "activate": "应用连接设置", "association": "等待无线关联", "address": "等待网络地址", "commit": "确认连接设置", "rollback": "恢复原设置", "retire": "退出旧无线连接", "reuse": "复用当前连接"}
			if label := labels[phase]; label != "" {
				fmt.Fprintln(stderr, label)
			}
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
