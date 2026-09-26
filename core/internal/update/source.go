package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

const releasesURL = "https://api.github.com/repos/" + Repository + "/releases?per_page=100"

var filenamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.~+-]{0,199}$`)

// Source has one origin: the official repository's immutable release assets.
// The only additional hosts are GitHub's asset redirect destinations. Proxies
// and caller-provided credentials are deliberately not inherited.
type Source struct {
	client *http.Client
}

func NewSource() *Source {
	return &Source{client: transport.NewReleaseClient(checkRedirect)}
}

func (s *Source) Close() { s.client.CloseIdleConnections() }

func checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Fragment != "" {
		return domain.Errorf(domain.CodeTransportFailure, "更新下载重定向无效")
	}
	switch request.URL.Host {
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return nil
	case "github.com":
		if releaseAssetURL(request.URL.String()) {
			return nil
		}
	}
	return domain.Errorf(domain.CodeTransportFailure, "更新下载重定向不在允许范围")
}

func releaseAssetURL(raw string) bool {
	u, err := url.Parse(raw)
	prefix := "/" + Repository + "/releases/download/"
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || !filenamePattern.MatchString(parts[1]) {
		return false
	}
	version, err := ParseVersion(parts[0])
	return err == nil && version.Major >= 2
}

func (s *Source) open(ctx context.Context, raw string) (*http.Response, error) {
	if raw != releasesURL && !releaseAssetURL(raw) {
		return nil, domain.Errorf(domain.CodePackageIncompatible, "更新来源不是官方发布地址")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "更新地址无效")
	}
	request.Header.Set("User-Agent", "smart-srun/2.0")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := s.client.Do(request)
	if err != nil {
		code := domain.CodeTransportFailure
		if errors.Is(ctx.Err(), context.Canceled) {
			code = domain.CodeCancelled
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = domain.CodeDeadlineExceeded
		}
		return nil, domain.Errorf(code, "无法从官方来源下载更新").Wrap(err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			return nil, domain.Errorf(domain.CodeNotFound, "该版本尚未提供 Go 发布清单或安装包")
		}
		return nil, domain.Errorf(domain.CodeTransportFailure, "更新服务器返回 HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (s *Source) read(ctx context.Context, raw string, limit int64) ([]byte, error) {
	response, err := s.open(ctx, raw)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, domain.Errorf(domain.CodePackageIncompatible, "发布元数据不完整或超出大小上限")
	}
	return data, nil
}

// Candidates returns at most ten newer releases, in numerical version order.
// Release metadata is an index, not authority for arbitrary download URLs.
func (s *Source) Candidates(ctx context.Context, current Version, channel string) ([]Version, error) {
	if channel == "" {
		channel = current.Channel()
	}
	if channel != "stable" && channel != "rc" {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "更新通道无效")
	}
	data, err := s.read(ctx, releasesURL, 4<<20)
	if err != nil {
		return nil, err
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(data, &releases); err != nil || releases == nil || len(releases) > 100 {
		return nil, domain.Errorf(domain.CodePackageIncompatible, "发布索引无效")
	}
	seen := make(map[Version]bool)
	var versions []Version
	for _, release := range releases {
		version, err := ParseVersion(release.Tag)
		if err != nil || release.Draft || version.Major != current.Major || version.Major < 2 || version.Compare(current) <= 0 ||
			release.Prerelease != (version.RC != 0) || (channel == "stable" && version.RC != 0) || seen[version] {
			continue
		}
		seen[version] = true
		versions = append(versions, version)
	}
	slices.SortFunc(versions, func(a, b Version) int { return b.Compare(a) })
	if len(versions) > 10 {
		versions = versions[:10]
	}
	return versions, nil
}

func (s *Source) Manifest(ctx context.Context, version Version) (Manifest, error) {
	raw := "https://github.com/" + Repository + "/releases/download/" + version.String() + "/release-manifest.json"
	data, err := s.read(ctx, raw, MaxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := ParseManifest(data)
	if err != nil || manifest.Release != version.String() {
		return Manifest{}, invalidManifest()
	}
	return manifest, nil
}

// Download writes a new private file and retains it only after the exact byte
// count, SHA256 and fsync succeed. It never extracts an archive or overwrites a
// caller's existing file. Package metadata/signatures are checked afterwards.
func (s *Source) Download(ctx context.Context, asset Asset, destination string) (resultErr error) {
	if asset.Bytes <= 0 || asset.Bytes > MaxAssetBytes || !sha256Pattern.MatchString(asset.SHA256) || !releaseAssetURL(asset.URL) {
		return invalidManifest()
	}
	response, err := s.open(ctx, asset.URL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != asset.Bytes {
		return domain.Errorf(domain.CodeChecksumMismatch, "安装包长度与发布清单不一致")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法创建更新下载文件").Wrap(err)
	}
	defer func() {
		file.Close()
		if resultErr != nil {
			os.Remove(destination)
		}
	}()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, asset.Bytes+1))
	if err != nil || count != asset.Bytes || hex.EncodeToString(hash.Sum(nil)) != asset.SHA256 {
		return domain.Errorf(domain.CodeChecksumMismatch, "安装包大小或 SHA256 校验失败")
	}
	if err := file.Sync(); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法保存更新下载文件").Wrap(err)
	}
	if err := file.Close(); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法关闭更新下载文件").Wrap(err)
	}
	return nil
}
