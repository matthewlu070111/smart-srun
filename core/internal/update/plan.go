package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// Inventory comes from the package database, not the router model or hostname.
type Inventory struct {
	PackageManager string            `json:"package_manager"`
	Architecture   string            `json:"architecture"`
	FirmwareFamily string            `json:"firmware_family"`
	DisplayVersion string            `json:"display_version"`
	Packages       map[string]string `json:"packages"`
}

func (i Inventory) Mode() (string, error) {
	_, core := i.Packages["smart-srun"]
	_, luci := i.Packages["luci-app-smart-srun"]
	_, bundle := i.Packages["luci-app-smart-srun-bundle"]
	if (bundle && (core || luci)) || (!bundle && !core) {
		return "", domain.Errorf(domain.CodePackageIncompatible, "安装包型不完整或互相冲突，请先修复包数据库")
	}
	if bundle {
		return "bundle", nil
	}
	if luci {
		if i.Packages["smart-srun"] != i.Packages["luci-app-smart-srun"] {
			return "", domain.Errorf(domain.CodeRecoveryRequired, "核心与界面版本不一致，请先修复上次安装")
		}
		return "split", nil
	}
	return "core", nil
}

type Plan struct {
	ID             string    `json:"plan_id"`
	Release        string    `json:"release"`
	SourceCommit   string    `json:"source_commit"`
	InstallMode    string    `json:"install_mode"`
	Inventory      Inventory `json:"inventory"`
	Assets         []Asset   `json:"assets"`
	DownloadBytes  int64     `json:"download_bytes"`
	InstalledBytes int64     `json:"installed_bytes"`
}

func (i Inventory) Validate() error {
	current, err := ParseVersion(i.DisplayVersion)
	if err != nil {
		return err
	}
	if _, err := i.Mode(); err != nil {
		return err
	}
	if (i.PackageManager != "opkg" && i.PackageManager != "apk") ||
		!namePattern.MatchString(i.Architecture) || i.Architecture == "all" || i.Architecture == "noarch" || !familyPattern.MatchString(i.FirmwareFamily) {
		return domain.Errorf(domain.CodePackageIncompatible, "无法确定本设备的包管理器、架构或固件系列")
	}
	for name, native := range i.Packages {
		if name != "smart-srun" && name != "luci-app-smart-srun" && name != "luci-app-smart-srun-bundle" {
			return domain.Errorf(domain.CodePackageIncompatible, "更新清单包含无关的已安装包")
		}
		prefix := current.NativeBase(i.PackageManager) + "-r"
		if !strings.HasPrefix(native, prefix) || !positiveNumber(strings.TrimPrefix(native, prefix)) {
			return domain.Errorf(domain.CodePackageIncompatible, "运行版本与已安装包版本不一致，请先重启或修复服务")
		}
	}
	return nil
}

// BuildPlan never changes package format, architecture, firmware family or
// bundle/split ownership as a side effect of an update. Stable installations
// stay on stable unless the caller explicitly selects the RC channel.
func BuildPlan(manifest Manifest, inventory Inventory, channel string) (Plan, error) {
	if err := manifest.Validate(); err != nil {
		return Plan{}, err
	}
	if err := inventory.Validate(); err != nil {
		return Plan{}, err
	}
	current, err := ParseVersion(inventory.DisplayVersion)
	if err != nil {
		return Plan{}, err
	}
	target, _ := ParseVersion(manifest.Release) // already validated above
	if target.Major != current.Major {
		return Plan{}, domain.Errorf(domain.CodePackageIncompatible, "跨主版本更新需要单独确认配置兼容性")
	}
	if channel == "" {
		channel = current.Channel()
	}
	if (channel != "stable" && channel != "rc") || (channel == "stable" && target.RC != 0) {
		return Plan{}, domain.Errorf(domain.CodePackageIncompatible, "该版本不属于当前更新通道")
	}
	if target.Compare(current) <= 0 {
		return Plan{}, domain.Errorf(domain.CodeNotFound, "没有更新的兼容版本")
	}
	mode, err := inventory.Mode()
	if err != nil {
		return Plan{}, err
	}
	kinds := []string{"core"}
	if mode == "bundle" {
		kinds = []string{"bundle"}
	} else if mode == "split" {
		kinds = append(kinds, "luci")
	}
	inventory.Packages = maps.Clone(inventory.Packages)
	plan := Plan{Release: manifest.Release, SourceCommit: manifest.SourceCommit, InstallMode: mode, Inventory: inventory}
	for _, kind := range kinds {
		matches := compatibleAssets(manifest.Assets, inventory, kind)
		if len(matches) != 1 {
			return Plan{}, domain.Errorf(domain.CodePackageIncompatible, "未找到唯一且已完成基本验证的兼容安装包")
		}
		asset := matches[0]
		asset.FirmwareCompat = slices.Clone(asset.FirmwareCompat)
		plan.Assets = append(plan.Assets, asset)
		plan.DownloadBytes += asset.Bytes
		plan.InstalledBytes += asset.InstalledBytes
	}
	if len(plan.Assets) == 2 && plan.Assets[0].PackageVersion != plan.Assets[1].PackageVersion {
		return Plan{}, domain.Errorf(domain.CodePackageIncompatible, "核心与界面安装包版本不匹配")
	}
	// LuCI + core together count against the same device payload limit.
	// The message reads the constant rather than repeating it: the previous
	// literal said 10 MiB and would have kept saying so after D80 raised it.
	if plan.InstalledBytes > MaxPayloadBytes {
		return Plan{}, domain.Errorf(domain.CodePackageIncompatible,
			"安装后的总载荷超过 %d MiB 限制", MaxPayloadBytes>>20)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, domain.Errorf(domain.CodeInternal, "无法记录更新计划").Wrap(err)
	}
	digest := sha256.Sum256(data)
	plan.ID = hex.EncodeToString(digest[:])
	return plan, nil
}

// RecoveryAssets selects the exact currently installed native versions. A
// nearby release or a different package revision is not an adequate backup.
func RecoveryAssets(manifest Manifest, inventory Inventory) ([]Asset, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.Release != inventory.DisplayVersion {
		return nil, invalidManifest()
	}
	mode, err := inventory.Mode()
	if err != nil {
		return nil, err
	}
	kinds := []string{"core"}
	if mode == "bundle" {
		kinds = []string{"bundle"}
	} else if mode == "split" {
		kinds = append(kinds, "luci")
	}
	var assets []Asset
	for _, kind := range kinds {
		matches := compatibleAssets(manifest.Assets, inventory, kind)
		if len(matches) != 1 || matches[0].PackageVersion != inventory.Packages[matches[0].PackageName()] {
			return nil, domain.Errorf(domain.CodePackageIncompatible, "找不到当前精确版本的恢复安装包")
		}
		asset := matches[0]
		asset.FirmwareCompat = slices.Clone(asset.FirmwareCompat)
		assets = append(assets, asset)
	}
	return assets, nil
}

func compatibleAssets(assets []Asset, inventory Inventory, kind string) []Asset {
	var found []Asset
	for _, asset := range assets {
		arch := inventory.Architecture
		if kind == "luci" {
			arch = "all"
			if inventory.PackageManager == "apk" {
				arch = "noarch"
			}
		}
		if asset.Kind == kind && asset.PackageManager == inventory.PackageManager &&
			asset.OpenWrtArch == arch && slices.Contains(asset.FirmwareCompat, inventory.FirmwareFamily) &&
			asset.Validation.Build && (kind == "luci" || asset.Validation.ELF) {
			found = append(found, asset)
		}
	}
	return found
}
