package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func newKey() string { return "cli-" + rand.Text() }

func commandFlags() *flag.FlagSet {
	f := flag.NewFlagSet("srunnet", flag.ContinueOnError)
	f.SetOutput(io.Discard) // Never echo an unknown argument that might be a secret.
	return f
}

func onlineDetect(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	usage := func() int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument,
			"用法：detect env|acid|operators|identity --access-mode wired|wifi --iface 接口 [--base-url URL --ac-id ID --ssid SSID --user-id 账号 --json --no-wait]；detect operator|verify --stdin [--json --no-wait]；detect status|cancel 任务ID"))
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "wifi" {
		return onlineWifi(ctx, client, args[1:], stdin, stdout, stderr)
	}
	if args[0] == "status" || args[0] == "cancel" {
		if len(args) != 2 {
			return usage()
		}
		method := "action.get"
		if args[0] == "cancel" {
			method = "action.cancel"
		}
		return printCall(ctx, client, method, daemon.ActionParams{ActionID: args[1]}, stdout, stderr)
	}
	methods := map[string]string{"env": "detect.environment", "acid": "detect.acid", "operators": "detect.operators", "identity": "detect.identity", "operator": "detect.verify", "verify": "detect.verify"}
	method := methods[args[0]]
	if method == "" {
		return usage()
	}
	f := commandFlags()
	asJSON, noWait, input, readOnly := f.Bool("json", false, ""), f.Bool("no-wait", false, ""), f.Bool("stdin", false, ""), f.Bool("read-only", false, "")
	var p daemon.VerificationParams
	f.StringVar(&p.BaseURL, "base-url", "", "")
	f.StringVar(&p.AccessMode, "access-mode", "", "")
	f.StringVar(&p.Iface, "iface", "", "")
	f.StringVar(&p.SSID, "ssid", "", "")
	f.StringVar(&p.ACID, "ac-id", "", "")
	f.StringVar(&p.UserID, "user-id", "", "")
	f.StringVar(&p.School, "school", "", "")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return usage()
	}
	if *readOnly {
		if args[0] != "operator" {
			return usage()
		}
		method = "detect.identity"
	}
	if *input {
		mixed := false
		f.Visit(func(v *flag.Flag) {
			if v.Name != "json" && v.Name != "no-wait" && v.Name != "stdin" && v.Name != "read-only" {
				mixed = true
			}
		})
		if mixed {
			return usage()
		}
		if file, ok := stdin.(*os.File); ok {
			if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "--stdin 需要管道或 JSON 文件输入"))
			}
		}
		data, err := io.ReadAll(io.LimitReader(stdin, (16<<10)+1))
		if err != nil || len(data) > 16<<10 {
			return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "探测 JSON 输入过长或无法读取"))
		}
		if method == "detect.identity" || method == "detect.verify" {
			err = control.DecodeParams(data, &p)
		} else {
			err = control.DecodeParams(data, &p.DetectACIDParams)
		}
		if err != nil {
			return onlineError(stderr, err)
		}
	} else if method == "detect.verify" {
		return usage()
	}
	if p.Session != "" {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "CLI 不接受 Web 会话参数"))
	}
	if p.Iface == "" || (p.AccessMode != "wired" && p.AccessMode != "wifi") || (p.AccessMode == "wifi" && p.SSID == "") || (method != "detect.environment" && p.BaseURL == "") {
		return usage()
	}
	if p.IdempotencyKey == "" {
		p.IdempotencyKey = newKey()
	}
	var params any = p.DetectACIDParams
	if method == "detect.identity" || method == "detect.verify" {
		params = p
	} else if p.UserID != "" {
		return usage()
	}
	return runTask(ctx, client, method, params, *asJSON, *noWait, 225*time.Second, stdout, stderr)
}

// runTask keeps submission separate from success. A completed discovery can
// still have found no answer; scripts receive that result and a nonzero exit.
func runTask(ctx context.Context, client onlineClient, method string, params any, asJSON, noWait bool, budget time.Duration, stdout, stderr *os.File) int {
	ctx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	if err := client.ensure(ctx); err != nil {
		return onlineError(stderr, err)
	}
	raw, err := client.call(ctx, method, params)
	if err != nil {
		return onlineError(stderr, err)
	}
	var receipt daemon.SubmitResult
	if err = json.Unmarshal(raw, &receipt); err != nil || receipt.ActionID == "" {
		return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "服务未返回有效的动作编号"))
	}
	if noWait {
		if asJSON {
			return writeJSON(stdout, stderr, receipt)
		}
		fmt.Fprintf(stdout, "任务 %s 已提交；运行 srunnet detect status %s 查询结果。\n", receipt.ActionID, receipt.ActionID)
		return ExitOK
	}
	fmt.Fprintf(stderr, "任务 %s 已提交，等待执行结果。\n", receipt.ActionID)
	view, err := waitTask(ctx, client, receipt.ActionID, stderr)
	if err != nil {
		return onlineError(stderr, err)
	}
	if asJSON {
		if code := writeJSON(stdout, stderr, view); code != ExitOK {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "%s：%s（%s）\n", view.ID, view.Message, view.State)
		if len(view.Result) > 0 {
			if code := writeJSON(stdout, stderr, view.Result); code != ExitOK {
				return code
			}
		}
	}
	if application.State(view.State) != application.StateSucceeded {
		if view.Code != "" {
			return ExitCodeFor(domain.Errorf(view.Code, "任务未完成"))
		}
		if view.State == string(application.StateCancelled) {
			return ExitCancelled
		}
		return ExitActionFailed
	}
	var finding struct {
		OK *bool `json:"ok"`
	}
	if len(view.Result) > 0 {
		if json.Unmarshal(view.Result, &finding) != nil {
			return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "探测结果格式无效"))
		}
		if finding.OK != nil && !*finding.OK {
			return ExitActionFailed
		}
	}
	return ExitOK
}
