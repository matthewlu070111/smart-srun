package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func onlineLog(ctx context.Context, client onlineClient, args []string, stdout, stderr *os.File) int {
	usage := func() int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：log [tail|follow] [-n 1..1000] [--channel plugin|network] [--json]；log runtime [--json]；follow 仅支持文本输出"))
	}
	mode := "follow"
	explicitMode := false
	if len(args) > 0 && (args[0] == "tail" || args[0] == "follow" || args[0] == "runtime") {
		mode = args[0]
		args = args[1:]
		explicitMode = true
	}
	f := commandFlags()
	lines := f.Int("n", 100, "")
	channel := f.String("channel", "plugin", "")
	asJSON := f.Bool("json", false, "")
	if f.Parse(args) != nil || f.NArg() != 0 || *lines < 1 || *lines > 1000 || (*channel != "plugin" && *channel != "network") {
		return usage()
	}
	if mode == "runtime" {
		if f.NFlag() > 0 && (f.NFlag() != 1 || !*asJSON) {
			return usage()
		}
		return runtimeLog(ctx, client, stdout, stderr)
	}
	// Legacy `log -n N` is a one-shot tail. Explicit follow stays a stream.
	if !explicitMode {
		f.Visit(func(v *flag.Flag) {
			if v.Name == "n" || v.Name == "json" {
				mode = "tail"
			}
		})
	}
	if *asJSON && mode == "follow" {
		return usage()
	}
	var cursor uint64
	for {
		raw, err := client.call(ctx, "log.tail", daemon.LogTailParams{Channel: *channel, Lines: *lines, Cursor: cursor})
		if ctx.Err() != nil {
			return ExitCancelled
		}
		if err != nil {
			return onlineError(stderr, err)
		}
		var page daemon.LogTailResult
		if json.Unmarshal(raw, &page) != nil {
			return onlineError(stderr, domain.Errorf(domain.CodeProtocolInvalid, "日志响应格式无效"))
		}
		if *asJSON {
			return writeJSON(stdout, stderr, page)
		}
		if page.Dropped {
			fmt.Fprintln(stderr, "部分旧日志已不在缓存中。")
		}
		for _, line := range page.Lines {
			if _, err := fmt.Fprintln(stdout, line); err != nil {
				return ExitActionFailed
			}
		}
		if mode == "tail" {
			return ExitOK
		}
		cursor = page.Cursor
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ExitCancelled
		case <-timer.C:
		}
	}
}

func runtimeLog(ctx context.Context, client onlineClient, stdout, stderr *os.File) int {
	cfg, err := readOnlineConfig(ctx, client)
	if err != nil {
		return onlineError(stderr, err)
	}
	// An explicit allowlist: diagnostic output never includes credential fields.
	view := struct {
		Revision   uint64                     `json:"config_revision"`
		School     string                     `json:"school"`
		Account    string                     `json:"account_id"`
		AccessMode domain.AccessMode          `json:"access_mode"`
		Interface  string                     `json:"iface"`
		Login      config.EffectiveLoginShape `json:"login"`
	}{Revision: cfg.Revision, School: cfg.School, Account: cfg.Selection.ActiveCampusID}
	for _, account := range cfg.CampusAccounts {
		if account.ID == view.Account {
			view.AccessMode = account.AccessMode
			view.Interface = account.WiredIface
			if account.AccessMode == domain.AccessModeWiFi {
				view.Interface = cfg.STAIface
			}
			view.Login = config.EffectiveLogin(cfg, account)
			break
		}
	}
	return writeJSON(stdout, stderr, view)
}
