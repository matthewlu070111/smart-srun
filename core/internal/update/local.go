package update

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

type LocalPackage struct {
	Asset Asset
	Path  string
}

func VerifyFile(file LocalPackage) error {
	info, err := os.Lstat(file.Path)
	if err != nil || !filepath.IsAbs(file.Path) || !info.Mode().IsRegular() || info.Size() != file.Asset.Bytes ||
		file.Asset.Bytes <= 0 || file.Asset.Bytes > MaxAssetBytes || !sha256Pattern.MatchString(file.Asset.SHA256) {
		return domain.Errorf(domain.CodeChecksumMismatch, "安装包文件、长度或类型不符")
	}
	input, err := os.Open(file.Path)
	if err != nil {
		return domain.Errorf(domain.CodeChecksumMismatch, "无法读取待校验安装包").Wrap(err)
	}
	defer input.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(input, file.Asset.Bytes+1))
	if err != nil || n != file.Asset.Bytes || hex.EncodeToString(hash.Sum(nil)) != file.Asset.SHA256 {
		return domain.Errorf(domain.CodeChecksumMismatch, "安装包 SHA256 校验失败")
	}
	return nil
}
