package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type configForm struct {
	ctx   context.Context
	input formInput
	err   error
}

func (f *configForm) ask(label, current string, secret, keep bool, limit int) string {
	if f.err != nil {
		return ""
	}
	if current != "" && !secret {
		label += " [" + strconv.Quote(current) + "]"
	}
	if keep {
		label += "（留空保留）"
	}
	value, err := f.input.Read(f.ctx, label, secret, limit)
	if err != nil {
		f.err = err
		return ""
	}
	if value == "" && keep {
		return current
	}
	return value
}
func (f *configForm) secret(label string, editing bool) *string {
	value := f.ask(label, "", true, editing, config.MaxSecretBytes)
	if editing && value == "" {
		return nil
	}
	return &value
}

func runInteractiveConfig(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	if len(args) < 2 || len(args) > 3 || (args[0] != "account" && args[0] != "hotspot") || (args[1] != "add" && args[1] != "edit") || (len(args) == 3 && args[1] != "edit") {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：config account|hotspot add|edit [ID] --interactive"))
	}
	input, err := newTerminalInput(stdin, stderr)
	if err != nil {
		return onlineError(stderr, err)
	}
	return interactiveConfig(ctx, client, args, input, stdout, stderr)
}

func interactiveConfig(ctx context.Context, client onlineClient, args []string, input formInput, stdout, stderr *os.File) int {
	if err := client.ensure(ctx); err != nil {
		return onlineError(stderr, err)
	}
	cfg, err := readOnlineConfig(ctx, client)
	if err != nil {
		return onlineError(stderr, err)
	}
	f := &configForm{ctx: ctx, input: input}
	editing := args[1] == "edit"
	verb := "新增"
	if editing {
		verb = "更新"
	}
	id := ""
	if editing {
		if len(args) == 3 {
			id = args[2]
		} else {
			id = f.ask("要编辑的 ID", "", false, false, config.MaxIDBytes)
		}
	}
	var method string
	var params any
	if args[0] == "account" {
		account := domain.CampusAccount{AccessMode: domain.AccessModeWired, WiredIface: config.DefaultWiredIface, Encryption: "none"}
		if editing {
			var found bool
			account, found = cfg.CampusAccountByID(id)
			if !found {
				if f.err != nil {
					return onlineError(stderr, f.err)
				}
				return onlineError(stderr, domain.Errorf(domain.CodeNotFound, "没有这个校园账号"))
			}
		}
		patch := config.CampusPatch{ID: id}
		label := f.ask("账号名称", account.Label, false, editing, config.MaxLabelBytes)
		user := f.ask("校园账号", account.UserID, false, editing, config.MaxUserIDBytes)
		patch.Password = f.secret("校园密码", editing)
		suffix := account.OperatorSuffix
		if !editing || strings.EqualFold(f.ask("更改认证后缀？输入 y 更改，其余保留", "", false, false, 8), "y") {
			suffix = f.ask("认证后缀（空行表示不加后缀）", "", false, false, config.MaxSuffixBytes)
		}
		mode := strings.TrimSpace(f.ask("接入方式 wired / wifi", string(account.AccessMode), false, true, 16))
		patch.Label, patch.UserID, patch.OperatorSuffix, patch.AccessMode = &label, &user, &suffix, &mode
		if mode == "wired" {
			iface := f.ask("有线出口接口", account.WiredIface, false, true, config.MaxNameBytes)
			if iface == "" {
				iface = config.DefaultWiredIface
			}
			patch.WiredIface = &iface
			if !editing {
				enabled := true
				patch.AuthEnabled = &enabled
			}
		} else if mode == "wifi" {
			ssid := f.ask("Wi-Fi 名称", account.SSID, false, editing, 32)
			radio := f.ask("无线电（留空自动选择）", account.Radio, false, editing, config.MaxNameBytes)
			encryption := f.ask("加密方式 none / psk2 / sae", account.Encryption, false, true, config.MaxNameBytes)
			patch.SSID, patch.Radio, patch.Encryption = &ssid, &radio, &encryption
			if config.KeyRequired(encryption) {
				patch.Key = f.secret("Wi-Fi 密码", editing)
			} else {
				empty := ""
				patch.Key = &empty
			}
		} else if f.err == nil {
			f.err = domain.Errorf(domain.CodeInvalidArgument, "接入方式只能是 wired 或 wifi")
		}
		baseURL := f.ask("认证地址（可稍后检测填写）", account.BaseURL, false, editing, config.MaxURLBytes)
		acid := f.ask("AC_ID（可稍后检测填写）", account.ACID, false, editing, config.MaxNameBytes)
		patch.BaseURL, patch.ACID = &baseURL, &acid
		method, params = "campus.upsert", daemon.CampusUpsertParams{ExpectedRevision: &cfg.Revision, Account: &patch}
		if f.err == nil {
			fmt.Fprintf(stderr, "将%s校园账号 %q，接入方式 %s，认证后缀 %q。\n", verb, user, mode, suffix)
		}
	} else {
		profile := domain.HotspotProfile{Encryption: "none"}
		if editing {
			var found bool
			profile, found = cfg.HotspotByID(id)
			if !found {
				if f.err != nil {
					return onlineError(stderr, f.err)
				}
				return onlineError(stderr, domain.Errorf(domain.CodeNotFound, "没有这个热点"))
			}
		}
		label := f.ask("热点名称", profile.Label, false, editing, config.MaxLabelBytes)
		ssid := f.ask("Wi-Fi 名称", profile.SSID, false, editing, 32)
		radio := f.ask("无线电（留空自动选择）", profile.Radio, false, editing, config.MaxNameBytes)
		encryption := f.ask("加密方式 none / psk2 / sae", profile.Encryption, false, true, config.MaxNameBytes)
		patch := config.HotspotPatch{ID: id, Label: &label, SSID: &ssid, Radio: &radio, Encryption: &encryption}
		if config.KeyRequired(encryption) {
			patch.Key = f.secret("Wi-Fi 密码", editing)
		} else {
			empty := ""
			patch.Key = &empty
		}
		method, params = "hotspot.upsert", daemon.HotspotUpsertParams{ExpectedRevision: &cfg.Revision, Profile: &patch}
		if f.err == nil {
			fmt.Fprintf(stderr, "将%s热点 %q，加密方式 %q。\n", verb, ssid, encryption)
		}
	}
	if f.err != nil {
		return onlineError(stderr, f.err)
	}
	confirmation := f.ask("保存以上配置？输入 y 保存，其余取消", "", false, false, 8)
	if f.err != nil {
		return onlineError(stderr, f.err)
	}
	if !strings.EqualFold(confirmation, "y") {
		return onlineError(stderr, domain.Errorf(domain.CodeCancelled, "已取消，未保存配置"))
	}
	return printCall(ctx, client, method, params, stdout, stderr)
}
