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

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func onlineWifi(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	usage := func() int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：detect wifi start --stdin [--json --no-wait]；detect wifi status|cancel 任务ID [--json]；detect wifi account|commit --stdin"))
	}
	command := "start"
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || args[0] == "--status" || args[0] == "--cancel") {
		command = strings.TrimPrefix(args[0], "--")
		args = args[1:]
	}
	job := ""
	if command == "status" || command == "cancel" {
		if len(args) == 0 {
			return usage()
		}
		job, args = args[0], args[1:]
	}
	f := commandFlags()
	input, noWait, asJSON := f.Bool("stdin", false, ""), f.Bool("no-wait", false, ""), f.Bool("json", false, "")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return usage()
	}
	var params any = struct {
		Job string `json:"job"`
	}{job}
	if command == "start" || command == "account" || command == "commit" {
		if !*input {
			return usage()
		}
		if file, ok := stdin.(*os.File); ok {
			if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				return usage()
			}
		}
		body, err := io.ReadAll(io.LimitReader(stdin, (16<<10)+1))
		if err != nil || len(body) > 16<<10 {
			return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "无线向导 JSON 输入过长或无法读取"))
		}
		if command == "start" {
			var p daemon.WifiSetupParams
			if err := control.DecodeParams(body, &p); err != nil {
				return onlineError(stderr, err)
			}
			if p.Session != "" {
				return usage()
			}
			if p.Job == "" {
				var key [16]byte
				if _, err := rand.Read(key[:]); err != nil {
					return onlineError(stderr, err)
				}
				p.Job = hex.EncodeToString(key[:])
			}
			job, params = p.Job, p
			if err := client.ensure(ctx); err != nil {
				return onlineError(stderr, err)
			}
		} else {
			var p struct {
				Job              string              `json:"job"`
				ExpectedRevision *uint64             `json:"expected_revision"`
				Account          *config.CampusPatch `json:"account"`
			}
			if err := config.DecodePatch(body, &p, "account.login.double_stack"); err != nil {
				return onlineError(stderr, err)
			}
			if p.Job == "" || p.ExpectedRevision == nil || p.Account == nil || *noWait {
				return usage()
			}
			return printCall(ctx, client, "setup_wifi."+command, p, stdout, stderr)
		}
	} else if (command != "status" && command != "cancel") || *input {
		return usage()
	}
	ctx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	interrupted := func() int {
		if command != "status" {
			undo, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, cancelErr := client.call(undo, "setup_wifi.cancel", struct {
				Job string `json:"job"`
			}{job})
			cancel()
			if cancelErr != nil {
				fmt.Fprintf(stderr, "未能确认无线任务 %s 已取消，请查询状态。\n", job)
			}
		}
		code := domain.CodeCancelled
		if ctx.Err() == context.DeadlineExceeded {
			code = domain.CodeDeadlineExceeded
		}
		return onlineError(stderr, domain.Errorf(code, "等待无线连接已中止"))
	}
	raw, err := client.call(ctx, "setup_wifi."+command, params)
	if ctx.Err() != nil {
		return interrupted()
	}
	if err != nil {
		return onlineError(stderr, err)
	}
	for {
		var view daemon.WifiSetupView
		if json.Unmarshal(raw, &view) != nil || view.Job != job || view.State == "" {
			return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "无线向导响应无效"))
		}
		done := view.State == "failed" || view.State == "cancelled" || view.State == "done" || (command == "start" && (view.State == "ready" || view.State == "connected"))
		if command == "status" || *noWait || done {
			if *asJSON {
				if code := writeJSON(stdout, stderr, view); code != ExitOK {
					return code
				}
			} else {
				fmt.Fprintf(stdout, "无线任务 %s：%s（%s）\n", view.Job, view.Message, view.State)
			}
			if !view.OK {
				return ExitActionFailed
			}
			return ExitOK
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return interrupted()
		case <-timer.C:
		}
		raw, err = client.call(ctx, "setup_wifi.status", struct {
			Job string `json:"job"`
		}{job})
		if ctx.Err() != nil {
			return interrupted()
		}
		if err != nil {
			return onlineError(stderr, err)
		}
	}
}
