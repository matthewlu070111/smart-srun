package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func runtimeRecordError(err error) error {
	return domain.Errorf(domain.CodeInternal, "无法保存或读取临时状态记录").Wrap(err)
}

func readRuntimeRecord(path string, target any) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != RuntimeFileMode || info.Size() > 4096 {
		return false, runtimeRecordError(err)
	}
	f, err := os.Open(path)
	if err != nil {
		return false, runtimeRecordError(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return false, runtimeRecordError(err)
	}
	if err := config.DecodePatch(data, target); err != nil {
		return false, runtimeRecordError(err)
	}
	return true, nil
}

func writeRuntimeRecord(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 4096 {
		return runtimeRecordError(err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-record-*")
	if err != nil {
		return runtimeRecordError(err)
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if err := f.Chmod(RuntimeFileMode); err != nil {
		return runtimeRecordError(err)
	}
	if _, err := f.Write(data); err != nil {
		return runtimeRecordError(err)
	}
	if err := f.Close(); err != nil {
		return runtimeRecordError(err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return runtimeRecordError(err)
	}
	return nil
}
