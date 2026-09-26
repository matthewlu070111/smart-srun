package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/daemon"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func onlineSchools(args []string, stdout, stderr *os.File) int {
	// The registry school_extra filtering and the daemon's schools.* read, so
	// the CLI cannot list a strategy the daemon would treat as unknown.
	registry := config.SchoolRegistry
	if len(args) == 1 && args[0] == "list" {
		for _, school := range registry.List() {
			fmt.Fprintf(stdout, "%s\t%s\n", school.ID, school.Label)
		}
		return ExitOK
	}
	if len(args) == 2 && args[0] == "list" && args[1] == "--json" {
		return writeJSON(stdout, stderr, registry.List())
	}
	if (len(args) == 2 || len(args) == 3 && args[2] == "--json") && args[0] == "inspect" {
		if school, ok := registry.Lookup(args[1]); ok {
			return writeJSON(stdout, stderr, school)
		}
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "没有这个认证策略；学校参数预设请运行 presets list"))
	}
	return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：schools list [--json]；schools inspect 策略ID [--json]"))
}

func onlinePresets(ctx context.Context, client onlineClient, args []string, stdout, stderr *os.File) int {
	usage := func() int {
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "用法：presets list [--all --json]；presets inspect ID [--json]；presets refresh --iface 接口 [--json --no-wait]"))
	}
	if len(args) < 2 {
		return usage()
	}
	f := commandFlags()
	asJSON := f.Bool("json", false, "")
	if args[1] == "refresh" && args[0] == "presets" {
		iface := f.String("iface", "", "")
		noWait := f.Bool("no-wait", false, "")
		if f.Parse(args[2:]) != nil || f.NArg() != 0 || *iface == "" {
			return usage()
		}
		return runTask(ctx, client, "presets.refresh", daemon.PresetRefreshParams{Interface: *iface, IdempotencyKey: newKey()}, *asJSON, *noWait, 75*time.Second, stdout, stderr)
	}
	all := f.Bool("all", false, "")
	remaining := args[2:]
	id := ""
	if args[1] == "inspect" {
		if len(remaining) == 0 {
			return usage()
		}
		id = remaining[0]
		remaining = remaining[1:]
	} else if args[1] != "list" {
		return usage()
	}
	if f.Parse(remaining) != nil || f.NArg() != 0 {
		return usage()
	}
	result, err := readPresets(ctx, client, *all || id != "")
	if err != nil {
		return onlineError(stderr, err)
	}
	if id != "" {
		for _, group := range [][]daemon.PresetView{result.User, result.Public} {
			for _, p := range group {
				if p.ShortName == id {
					return writeJSON(stdout, stderr, p)
				}
			}
		}
		return onlineError(stderr, domain.Errorf(domain.CodeInvalidArgument, "没有这个预设，请运行 presets list --all"))
	}
	if *asJSON {
		return writeJSON(stdout, stderr, result)
	}
	for _, group := range [][]daemon.PresetView{result.Public, result.User} {
		for _, p := range group {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", p.ShortName, p.Name, p.Status)
		}
	}
	fmt.Fprintf(stdout, "共 %d 个学校预设。\n", len(result.Public)+len(result.User))
	return ExitOK
}

func readPresets(ctx context.Context, client onlineClient, all bool) (daemon.PresetListResult, error) {
	result := daemon.PresetListResult{Public: []daemon.PresetView{}, User: []daemon.PresetView{}}
	offset := 0
	for pageNumber := 0; pageNumber < 128; pageNumber++ {
		raw, err := client.call(ctx, "presets.list", daemon.PresetListParams{Offset: offset, Limit: 100, IncludeInactive: all})
		if err != nil {
			return result, err
		}
		var page daemon.PresetListResult
		if json.Unmarshal(raw, &page) != nil {
			return result, domain.Errorf(domain.CodeProtocolInvalid, "预设列表格式无效")
		}
		if pageNumber > 0 && page.Revision != result.Revision {
			return result, domain.Errorf(domain.CodeConflict, "预设读取期间发生编辑，请重试")
		}
		result.Public = append(result.Public, page.Public...)
		result.User = append(result.User, page.User...)
		result.Operators, result.Revision, result.Total = page.Operators, page.Revision, page.Total
		if page.NextOffset == nil {
			return result, nil
		}
		if *page.NextOffset <= offset {
			return result, domain.Errorf(domain.CodeProtocolInvalid, "预设列表分页未前进")
		}
		offset = *page.NextOffset
	}
	return result, domain.Errorf(domain.CodeProtocolInvalid, "预设列表超过分页上限")
}
