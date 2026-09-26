package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func onlineBackup(ctx context.Context, client onlineClient, args []string, stdin io.Reader, stdout, stderr *os.File) int {
	fail := func(message string) int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "%s", message))
	}
	if len(args) < 2 {
		return fail("用法：config export FILE|-；config import FILE|- [--check] [--expected-revision N]")
	}
	if args[0] == "export" {
		if len(args) != 2 {
			return fail("用法：config export FILE|-（备份包含密码）")
		}
		raw, err := client.call(ctx, "config.export", map[string]bool{"include_secrets": true})
		if err != nil {
			return onlineError(stderr, err)
		}
		if args[1] == "-" {
			return writeJSON(stdout, stderr, raw)
		}
		file, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail("无法创建备份；请选择不存在的文件路径")
		}
		_, err = file.Write(append(raw, '\n'))
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return fail("备份写入失败，文件可能不完整")
		}
		return writeJSON(stdout, stderr, json.RawMessage(`{"ok":true,"message":"已导出含密码的配置备份，请妥善保管"}`))
	}
	params := daemon.BackupImportParams{}
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--check":
			params.CheckOnly = true
		case "--expected-revision":
			i++
			if i >= len(args) {
				return fail("需要配置版本")
			}
			value, err := strconv.ParseUint(args[i], 10, 64)
			if err != nil {
				return fail("配置版本无效")
			}
			params.ExpectedRevision = &value
		default:
			return fail("未知导入参数")
		}
	}
	if args[1] != "-" {
		file, err := os.Open(args[1])
		if err != nil {
			return fail("无法读取备份文件")
		}
		defer file.Close()
		stdin = file
	}
	data, err := io.ReadAll(io.LimitReader(stdin, config.MaxConfigBytes+1))
	if err != nil {
		return fail("无法读取备份")
	}
	if _, _, err := config.ParseBackup(data); err != nil {
		return onlineError(stderr, err)
	}
	params.Data = string(data)
	if !params.CheckOnly && params.ExpectedRevision == nil {
		cfg, err := readOnlineConfig(ctx, client)
		if err != nil {
			return onlineError(stderr, err)
		}
		params.ExpectedRevision = &cfg.Revision
	}
	// Do not start an unavailable service merely to import credentials: starting
	// it could authenticate with the old enabled configuration before replacement.
	return printCall(ctx, client, "config.import", params, stdout, stderr)
}
