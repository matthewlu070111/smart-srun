package update

import (
	"io"
	"os"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// LocalCandidate is an explicit root CLI/SSH deployment path. The RPC accepts
// only its saved plan ID; it cannot import a manifest or choose filesystem
// paths. Every package still goes through hash, native metadata and signature
// verification, and the independent worker performs the same transaction.
func LocalCandidate(manifest, recovery Manifest, inventory Inventory) (Candidate, error) {
	plan, err := BuildPlan(manifest, inventory, "rc")
	if err != nil {
		return Candidate{}, err
	}
	assets, err := RecoveryAssets(recovery, inventory)
	if err != nil {
		return Candidate{}, err
	}
	recovery.Assets = assets
	return Candidate{Plan: plan, Recovery: recovery, Local: true}, nil
}

func LocalInputs(paths Paths, candidate Candidate) []LocalPackage {
	files := packageFiles(filepath.Join(paths.Inbox(), "new"), candidate.Plan.Assets)
	return append(files, packageFiles(filepath.Join(paths.Inbox(), "recovery"), candidate.Recovery.Assets)...)
}

func stageLocal(paths Paths, task Task) error {
	for _, set := range []struct {
		source, destination string
		assets              []Asset
	}{
		{filepath.Join(paths.Inbox(), "new"), paths.Temporary(task.JobID), task.Plan.Assets},
		{filepath.Join(paths.Inbox(), "recovery"), paths.Backup(task.JobID), task.Recovery.Assets},
	} {
		if err := privateDir(set.destination); err != nil {
			return err
		}
		for _, input := range packageFiles(set.source, set.assets) {
			if err := VerifyFile(input); err != nil {
				return err
			}
			destination := filepath.Join(set.destination, filepath.Base(input.Path))
			if err := copyPackage(input, destination); err != nil {
				return err
			}
		}
		if err := syncDirectory(set.destination); err != nil {
			return err
		}
	}
	// Both private destinations have been copied, hashed and synced. Consume
	// only this plan's verified upload files; retaining the inbox duplicates
	// several MiB on tmpfs after every successful SSH deployment.
	for _, input := range LocalInputs(paths, Candidate{Plan: task.Plan, Recovery: task.Recovery}) {
		if err := VerifyFile(input); err != nil {
			return err
		}
		if err := os.Remove(input.Path); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func copyPackage(input LocalPackage, destination string) (result error) {
	file, err := os.Open(input.Path)
	if err != nil {
		return storageError(err)
	}
	defer file.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return storageError(err)
	}
	defer func() {
		output.Close()
		if result != nil {
			os.Remove(destination)
		}
	}()
	n, err := io.Copy(output, io.LimitReader(file, input.Asset.Bytes+1))
	if err != nil || n != input.Asset.Bytes {
		return domain.Errorf(domain.CodeChecksumMismatch, "本地安装包在复制过程中变化")
	}
	if err := output.Sync(); err != nil {
		return storageError(err)
	}
	if err := output.Close(); err != nil {
		return storageError(err)
	}
	return VerifyFile(LocalPackage{input.Asset, destination})
}
