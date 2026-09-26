package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Version is stamped at build time by the packaging Makefile. The checked-in
// value is 0.0.0, matching how the OpenWrt package already handles versioning.
var Version = "0.0.0"

// VersionString is what `srunnet version` prints.
func VersionString() string {
	return fmt.Sprintf("srunnet %s (config schema v%d)",
		Version, domain.ConfigSchemaVersion)
}

// RunConfig dispatches the config sub-commands this build implements.
func RunConfig(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法：srunnet config validate|schema|defaults")
		return ExitInvalidInput
	}
	switch args[0] {
	case "validate":
		return runConfigValidate(args[1:], stdout, stderr)
	case "schema":
		return writeJSON(stdout, stderr, config.BuildSchema())
	case "defaults":
		return writeJSON(stdout, stderr, config.Defaults())
	default:
		fmt.Fprintf(stderr, "未知子命令 %q，可用：validate、schema、defaults\n", args[0])
		return ExitInvalidInput
	}
}

// runConfigValidate checks a document without touching the installed
// configuration. It runs offline by design: the spec requires `config validate`
// to work with the daemon stopped.
func runConfigValidate(args []string, stdout, stderr *os.File) int {
	var (
		data   []byte
		err    error
		source string
	)
	switch len(args) {
	case 0:
		source = "标准输入"
		data, err = io.ReadAll(io.LimitReader(os.Stdin, config.MaxConfigBytes+1))
	case 1:
		source = args[0]
		data, err = os.ReadFile(args[0])
	default:
		fmt.Fprintln(stderr, "用法：srunnet config validate [文件]")
		return ExitInvalidInput
	}
	if err != nil {
		fmt.Fprintf(stderr, "读取 %s 失败：%v\n", source, err)
		return ExitInvalidInput
	}

	// The same decode-normalize-validate pipeline the repository uses. Spelling
	// it out again here is how the CLI ends up accepting a file the daemon
	// would then refuse.
	cfg, err := config.Parse(data)
	if err != nil {
		reportProblems(stderr, source, err)
		return ExitCodeFor(err)
	}

	fmt.Fprintf(stdout, "%s：配置有效（%d 个校园账号，%d 个热点）\n",
		source, len(cfg.CampusAccounts), len(cfg.HotspotProfiles))
	return ExitOK
}

// reportProblems prints every problem, one per line, so a user fixing a file
// sees the whole list instead of discovering them one run at a time.
func reportProblems(stderr *os.File, source string, err error) {
	fmt.Fprintf(stderr, "%s：配置无效\n", source)
	if set, ok := err.(*domain.Errors); ok {
		for _, item := range set.Items {
			if item.Field == "" {
				fmt.Fprintf(stderr, "  - %s\n", item.Message)
				continue
			}
			fmt.Fprintf(stderr, "  - %s：%s\n", item.Field, item.Message)
		}
		return
	}
	fmt.Fprintf(stderr, "  - %v\n", err)
}

// writeJSON keeps stdout to exactly one valid document, as the CLI contract
// requires for machine-readable output; diagnostics go to stderr.
func writeJSON(stdout, stderr *os.File, payload any) int {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "序列化失败：%v\n", err)
		return ExitActionFailed
	}
	stdout.Write(append(encoded, '\n'))
	return ExitOK
}
