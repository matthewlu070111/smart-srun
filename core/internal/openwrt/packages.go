package openwrt

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

var projectPackages = []string{"smart-srun", "luci-app-smart-srun", "luci-app-smart-srun-bundle"}

type installedPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// PackageDevice only reads the native database here. Installing packages is a
// different operation, performed by the separately supervised update worker.
type PackageDevice struct {
	Runner       Runner
	Capabilities Capabilities
}

// CurrentPackageInventory does not reuse startup capability failures. Native
// postinst can start the daemon while opkg still holds its database lock, so
// that transient failure must not disable updates for the process lifetime.
func CurrentPackageInventory(ctx context.Context, runner Runner, displayVersion string) (update.Inventory, error) {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	capabilities := Detect(probe, runner)
	cancel()
	return (PackageDevice{Runner: runner, Capabilities: capabilities}).Inventory(ctx, displayVersion)
}

func (d PackageDevice) Inventory(ctx context.Context, displayVersion string) (update.Inventory, error) {
	manager := d.Capabilities.PackageManager
	if manager != PackageManagerAPK && manager != PackageManagerOpkg {
		return update.Inventory{}, domain.Errorf(domain.CodeUnsupportedCapability, "没有可用的包管理器")
	}
	board, err := d.Runner.Run(ctx, "ubus", "call", "system", "board")
	if err != nil || board.StdoutTruncated {
		return update.Inventory{}, domain.Errorf(domain.CodePackageIncompatible, "无法读取设备固件信息")
	}
	var system struct {
		Release struct {
			Version string `json:"version"`
		} `json:"release"`
	}
	if json.Unmarshal(board.Stdout, &system) != nil || len(system.Release.Version) < 5 {
		return update.Inventory{}, domain.Errorf(domain.CodePackageIncompatible, "设备未提供明确的固件系列")
	}
	packages, err := d.queryPackages(ctx, false)
	if err != nil {
		return update.Inventory{}, err
	}
	inventory := update.Inventory{PackageManager: string(manager), DisplayVersion: displayVersion,
		FirmwareFamily: system.Release.Version[:5], Packages: make(map[string]string)}
	for _, pkg := range packages {
		if !slices.Contains(projectPackages, pkg.Name) || pkg.Version == "" || inventory.Packages[pkg.Name] != "" {
			return update.Inventory{}, domain.Errorf(domain.CodePackageIncompatible, "包数据库包含重复或无效条目")
		}
		inventory.Packages[pkg.Name] = pkg.Version
		if pkg.Name != "luci-app-smart-srun" {
			if pkg.Arch == "all" || pkg.Arch == "noarch" || !slices.Contains(d.Capabilities.PackageArchitectures, pkg.Arch) ||
				(inventory.Architecture != "" && inventory.Architecture != pkg.Arch) {
				return update.Inventory{}, domain.Errorf(domain.CodePackageIncompatible, "已安装核心的架构与包管理器不兼容")
			}
			inventory.Architecture = pkg.Arch
		}
	}
	if _, err := inventory.Mode(); err != nil {
		return update.Inventory{}, err
	}
	return inventory, nil
}

func (d PackageDevice) queryPackages(ctx context.Context, reportIncomplete bool) ([]installedPackage, error) {
	manager := d.Capabilities.PackageManager
	var packages []installedPackage
	if manager == PackageManagerAPK {
		result, runErr := d.Runner.Run(ctx, "apk", "--network=no", "query", "--from", "installed",
			"--installed", "--format", "json", "--fields", "name,version,arch",
			projectPackages[0], projectPackages[1], projectPackages[2])
		if runErr != nil || result.StdoutTruncated || json.Unmarshal(result.Stdout, &packages) != nil {
			return nil, domain.Errorf(domain.CodePackageIncompatible, "无法读取 APK 已安装包数据库")
		}
	} else if manager == PackageManagerOpkg {
		for _, name := range projectPackages {
			result, runErr := d.Runner.Run(ctx, "opkg", "status", name)
			if runErr != nil || result.StdoutTruncated {
				return nil, domain.Errorf(domain.CodePackageIncompatible, "无法读取 opkg 已安装包数据库")
			}
			parsed, err := parseOpkgPackages(result.Stdout, reportIncomplete)
			if err != nil {
				return nil, err
			}
			packages = append(packages, parsed...)
		}
	}
	return packages, nil
}

// InstalledVersions does not require a matching split pair. After a partial
// install, the individual versions are more useful than a guessed rollback.
func (d PackageDevice) InstalledVersions(ctx context.Context) (map[string]string, error) {
	packages, err := d.queryPackages(ctx, true)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string)
	for _, pkg := range packages {
		versions[pkg.Name] = pkg.Version
	}
	return versions, nil
}

func parseOpkgStatus(data []byte) ([]installedPackage, error) {
	return parseOpkgPackages(data, false)
}

func parseOpkgPackages(data []byte, reportIncomplete bool) ([]installedPackage, error) {
	var packages []installedPackage
	for _, paragraph := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(paragraph, "\n") {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			name, value, ok := strings.Cut(line, ":")
			if !ok {
				return nil, domain.Errorf(domain.CodePackageIncompatible, "opkg 状态格式无效")
			}
			if name != "Package" && name != "Version" && name != "Architecture" && name != "Status" {
				continue
			}
			if _, duplicate := fields[name]; duplicate {
				return nil, domain.Errorf(domain.CodePackageIncompatible, "opkg 状态字段重复")
			}
			fields[name] = strings.TrimSpace(value)
		}
		status := strings.Fields(fields["Status"])
		if len(status) == 0 {
			continue
		}
		if len(status) != 3 {
			return nil, domain.Errorf(domain.CodePackageIncompatible, "opkg 安装状态无效")
		}
		if status[2] != "installed" {
			if status[2] != "not-installed" && status[2] != "config-files" {
				if reportIncomplete {
					packages = append(packages, installedPackage{fields["Package"], fields["Version"] + " [" + status[2] + "]", fields["Architecture"]})
					continue
				}
				return nil, domain.Errorf(domain.CodeRecoveryRequired, "包处于未完成的安装状态，请先修复")
			}
			continue
		}
		packages = append(packages, installedPackage{fields["Package"], fields["Version"], fields["Architecture"]})
	}
	return packages, nil
}
