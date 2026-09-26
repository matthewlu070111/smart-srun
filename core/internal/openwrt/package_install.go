package openwrt

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/update"
)

func (d PackageDevice) Verify(ctx context.Context, file update.LocalPackage) error {
	if err := update.VerifyFile(file); err != nil {
		return err
	}
	var metadata update.PackageMetadata
	var err error
	if file.Asset.PackageManager != string(d.Capabilities.PackageManager) {
		return domain.Errorf(domain.CodePackageIncompatible, "安装包格式与当前包管理器不符")
	}
	if file.Asset.Format == "apk" {
		// The system's trust store remains authoritative. In particular there
		// is no allow-untrusted option and no task-supplied key directory.
		if _, err := d.Runner.Run(ctx, "apk", "--network=no", "verify", file.Path); err != nil {
			return domain.Errorf(domain.CodePackageIncompatible, "APK 签名或内容校验失败，请检查项目受信公钥").Wrap(err)
		}
		result, runErr := d.Runner.Run(ctx, "apk", "adbdump", "--format", "json", file.Path)
		if runErr != nil || result.StdoutTruncated {
			return domain.Errorf(domain.CodePackageIncompatible, "无法读取 APK 元数据")
		}
		metadata, err = update.InspectAPKMetadata(result.Stdout)
	} else {
		input, openErr := os.Open(file.Path)
		if openErr != nil {
			return domain.Errorf(domain.CodePackageIncompatible, "无法读取 IPK 文件").Wrap(openErr)
		}
		metadata, err = update.InspectIPK(input)
		input.Close()
	}
	if err != nil {
		return err
	}
	return metadata.Validate(file.Asset)
}

func (d PackageDevice) installArgs(files []update.LocalPackage, simulate bool) ([]string, error) {
	if len(files) == 0 || len(files) > 2 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "安装包数量无效")
	}
	var args []string
	switch d.Capabilities.PackageManager {
	case PackageManagerAPK:
		args = []string{"--network=no", "--progress=no", "--interactive=no", "--preserve-env=yes"}
		if simulate {
			args = append(args, "--simulate")
		}
		args = append(args, "add")
	case PackageManagerOpkg:
		if simulate {
			args = append(args, "--noaction")
		}
		args = append(args, "install")
	default:
		return nil, domain.Errorf(domain.CodeUnsupportedCapability, "没有可用的包管理器")
	}
	for _, file := range files {
		if !filepath.IsAbs(file.Path) || file.Asset.PackageManager != string(d.Capabilities.PackageManager) {
			return nil, domain.Errorf(domain.CodePackageIncompatible, "安装包路径或格式无效")
		}
		args = append(args, file.Path)
	}
	return args, nil
}

func (d PackageDevice) Precheck(ctx context.Context, files []update.LocalPackage) error {
	args, err := d.installArgs(files, true)
	if err != nil {
		return err
	}
	runner := d.Runner
	runner.Timeout = 30 * time.Second
	if _, err := runner.Run(ctx, string(d.Capabilities.PackageManager), args...); err != nil {
		return domain.Errorf(domain.CodePackageIncompatible, "包管理器预检查失败，尚未更改安装").Wrap(err)
	}
	return nil
}

// Install deliberately has no context or kill timeout. Once the native
// transaction begins, cancellation may not tear it apart. The independent
// worker waits for an actual exit and records recovery state on failure.
func (d PackageDevice) Install(files []update.LocalPackage) error {
	return d.install(files, false)
}

func (d PackageDevice) Recover(files []update.LocalPackage) error {
	return d.install(files, true)
}

func (d PackageDevice) install(files []update.LocalPackage, recover bool) error {
	args, err := d.installArgs(files, false)
	if err != nil {
		return err
	}
	if recover && d.Capabilities.PackageManager == PackageManagerOpkg {
		args = append([]string{"--force-downgrade", "--force-reinstall"}, args...)
	}
	if recover && d.Capabilities.PackageManager == PackageManagerAPK {
		return d.recoverAPK(files, args)
	}
	_, err = d.Runner.RunInstall(string(d.Capabilities.PackageManager), args...)
	return err
}
