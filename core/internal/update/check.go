package update

import (
	"context"
	"errors"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type ReleaseSource interface {
	Candidates(context.Context, Version, string) ([]Version, error)
	Manifest(context.Context, Version) (Manifest, error)
	Download(context.Context, Asset, string) error
}

type Candidate struct {
	Plan     Plan     `json:"plan"`
	Recovery Manifest `json:"recovery"`
	Local    bool     `json:"local"`
}

type CheckResult struct {
	OK              bool             `json:"ok"`
	Running         bool             `json:"running"`
	JobID           string           `json:"job_id,omitempty"`
	UpdateAvailable bool             `json:"update_available"`
	PlanID          string           `json:"plan_id,omitempty"`
	CurrentVersion  string           `json:"current_version"`
	LatestVersion   string           `json:"latest_version,omitempty"`
	LatestTag       string           `json:"latest_tag,omitempty"`
	InstallMode     string           `json:"install_mode,omitempty"`
	PackageFormat   string           `json:"package_format,omitempty"`
	Message         string           `json:"message"`
	Code            domain.ErrorCode `json:"code,omitempty"`
}

func Check(ctx context.Context, source ReleaseSource, inventory Inventory, channel string) (Candidate, CheckResult, error) {
	result := CheckResult{CurrentVersion: inventory.DisplayVersion}
	if err := inventory.Validate(); err != nil {
		return Candidate{}, result, err
	}
	current, err := ParseVersion(inventory.DisplayVersion)
	if err != nil {
		return Candidate{}, result, err
	}
	versions, err := source.Candidates(ctx, current, channel)
	if err != nil {
		return Candidate{}, result, err
	}
	var candidate Candidate
	for _, version := range versions {
		manifest, err := source.Manifest(ctx, version)
		if err != nil {
			if code, _ := domain.CodeOf(err); code == domain.CodeNotFound {
				continue
			}
			return candidate, result, err
		}
		plan, err := BuildPlan(manifest, inventory, channel)
		if err != nil {
			continue
		} // a release can have no asset for this device
		candidate.Plan = plan
		break
	}
	if candidate.Plan.ID == "" {
		result.OK = true
		result.Message = "没有更新的兼容版本"
		return candidate, result, nil
	}
	recovery, err := source.Manifest(ctx, current)
	if err != nil {
		return Candidate{}, result, domain.Errorf(domain.CodePackageIncompatible, "当前版本缺少恢复清单，暂不能自动更新").Wrap(err)
	}
	assets, err := RecoveryAssets(recovery, inventory)
	if err != nil {
		return Candidate{}, result, err
	}
	recovery.Assets = assets // journal contains only this installation's packages
	candidate.Recovery = recovery
	result.OK, result.UpdateAvailable, result.PlanID = true, true, candidate.Plan.ID
	result.LatestVersion, result.LatestTag = candidate.Plan.Release, candidate.Plan.Release
	result.InstallMode, result.PackageFormat = candidate.Plan.InstallMode, candidate.Plan.Assets[0].Format
	result.Message = "发现兼容更新"
	return candidate, result, nil
}

func SaveCandidate(paths Paths, candidate Candidate) error {
	if err := ValidatePlan(candidate.Plan); err != nil {
		return err
	}
	if _, err := RecoveryAssets(candidate.Recovery, candidate.Plan.Inventory); err != nil {
		return err
	}
	return writeState(paths.Plan(), candidate)
}

func ReadCandidate(paths Paths, id string) (Candidate, error) {
	var candidate Candidate
	if err := readState(paths.Plan(), &candidate); err != nil {
		return candidate, domain.Errorf(domain.CodeNotFound, "更新计划已失效，请重新检查更新")
	}
	if candidate.Plan.ID != id {
		return Candidate{}, domain.Errorf(domain.CodeConflict, "更新计划已变化，请重新检查")
	}
	if err := ValidatePlan(candidate.Plan); err != nil {
		return Candidate{}, err
	}
	if _, err := RecoveryAssets(candidate.Recovery, candidate.Plan.Inventory); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func ErrorStatus(err error) (domain.ErrorCode, string) {
	if typed, ok := errors.AsType[*domain.Error](err); ok {
		return typed.Code, typed.Message
	}
	return domain.CodeInternal, "更新失败，请检查本地恢复记录"
}
