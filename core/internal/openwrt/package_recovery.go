package openwrt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

// A power cut can leave old database entries with new files on disk. apk add
// alone skips the apparently installed version. First restore exact world
// constraints with add, then reinstall only those packages with apk fix.
//
// apk fix accepts package names, not archive paths. Supply a private native
// cache of links to the verified recovery archives. No extra package copy is
// allocated in RAM or flash; /tmp drops the links if recovery itself loses power.
func (d PackageDevice) recoverAPK(files []update.LocalPackage, addArgs []string) error {
	cache, err := os.MkdirTemp("", "smart-srun-apk-recovery-")
	if err != nil {
		return err
	}
	var links []string
	defer func() {
		for _, link := range links {
			os.Remove(link)
		}
		// apk owns this flat metadata file; never recursively remove unknown files.
		os.Remove(filepath.Join(cache, "installed"))
		os.Remove(cache)
	}()
	fixArgs := []string{"--network=no", "--progress=no", "--interactive=no", "--preserve-env=yes",
		"--repositories-file", "/dev/null", "--cache-dir", cache, "fix", "--reinstall"}
	for _, file := range files {
		info, err := os.Lstat(file.Path)
		if err != nil || !info.Mode().IsRegular() {
			return domain.Errorf(domain.CodePackageIncompatible, "恢复包必须是常规文件")
		}
		result, err := d.Runner.Run(context.Background(), "apk", "adbdump", "--format", "json", file.Path)
		if err != nil || result.StdoutTruncated {
			return domain.Errorf(domain.CodePackageIncompatible, "无法读取恢复包标识")
		}
		name, err := apkRecoveryCacheName(result.Stdout, file.Asset)
		if err != nil {
			return err
		}
		link := filepath.Join(cache, name)
		if err := os.Symlink(file.Path, link); err != nil {
			return err
		}
		links = append(links, link)
		fixArgs = append(fixArgs, file.Asset.PackageName())
	}
	// Disable repository indexes in both operations. Recovery must only select
	// the exact local backups and already installed dependencies, even offline.
	addArgs = append([]string{"--repositories-file", "/dev/null"}, addArgs...)
	if _, err := d.Runner.RunInstall("apk", addArgs...); err != nil {
		return err
	}
	_, err = d.Runner.RunInstall("apk", fixArgs...)
	return err
}

func apkRecoveryCacheName(data []byte, asset update.Asset) (string, error) {
	var doc struct {
		Info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Hashes  string `json:"hashes"`
		} `json:"info"`
	}
	if len(data) > 256<<10 || json.Unmarshal(data, &doc) != nil {
		return "", domain.Errorf(domain.CodePackageIncompatible, "恢复包标识格式无效")
	}
	i := doc.Info
	hash, err := hex.DecodeString(i.Hashes)
	if err != nil || (len(hash) != 20 && len(hash) != 32) || strings.ToLower(i.Hashes) != i.Hashes ||
		i.Name == "" || i.Name != asset.PackageName() || i.Version == "" || i.Version != asset.PackageVersion ||
		len(i.Version) > 128 || strings.ContainsAny(i.Version, "/\\\x00\r\n") {
		return "", domain.Errorf(domain.CodePackageIncompatible, "恢复包标识与清单不符")
	}
	// apk-tools 3 context.c default_cachename_spec: name-version.hash:8.apk.
	return i.Name + "-" + i.Version + "." + i.Hashes[:8] + ".apk", nil
}
