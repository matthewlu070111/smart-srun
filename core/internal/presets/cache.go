package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// FileMode and DirMode are what the cache is written with.
//
// A catalogue is public data -- it is fetched over the open internet by
// anybody -- so the mode is not protecting a secret. It matches the rest of
// what this program writes so that a directory listing has one answer rather
// than two, and the directory above it is what actually keeps other users out.
const (
	FileMode = 0o600
	DirMode  = 0o700
	// DefaultCachePath is tmpfs per spec 02. The built-in catalogue supplies
	// the offline fallback after reboot; refreshing must not wear out flash.
	DefaultCachePath = "/tmp/smart-srun/presets-cache.json"
	maxSourceBytes   = 8 << 10
	// Allow the largest payload plus JSON-escaped source metadata.
	maxCacheBytes = MaxPayloadBytes + 64<<10
)

// Cache is the last good catalogue. Production uses tmpfs; it is regenerable
// public data, not user configuration or a persistent recovery journal.
type Cache struct {
	path string
}

// NewCache addresses a cache file. It creates nothing: a router that never
// refreshes should not gain a directory for a file it will not write.
func NewCache(path string) *Cache {
	if path == "" {
		path = DefaultCachePath
	}
	return &Cache{path: path}
}

// Path is where it lives, for a caller that has to report it.
func (c *Cache) Path() string { return c.path }

// cached is the wrapper the file actually holds.
//
// The published document goes under its own key rather than having this
// program's bookkeeping merged into it. Adding _cached_at beside the
// publisher's own fields -- which is what the baseline does -- means a future
// schema that happens to use that name collides with it.
//
// Save appends the original payload without passing it through an encoder, so
// unknown fields, missing fields, field order and internal whitespace survive.
type cached struct {
	CachedAt  int64           `json:"cached_at"`
	SourceURL string          `json:"source_url"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Entry is what Load found: the catalogue and where it came from.
type Entry struct {
	Catalogue Catalogue
	SourceURL string
	CachedAt  time.Time
	// Raw is the published document as JSON, so a later build with a different
	// parser can read a cache this one wrote. Only surrounding JSON whitespace
	// is stripped when the wrapper is decoded; the payload is not re-encoded.
	Raw []byte
}

// Load reads the cache, reporting separately whether there was one.
//
// Three answers, not two. No file is the ordinary case on a fresh install and
// is not an error. A file this build cannot read is present and broken, and the
// caller is told both: it should fall back to the built-in catalogue rather
// than fail, but a cache that cannot be read is worth saying out loud instead
// of silently behaving like an empty one.
func (c *Cache) Load() (Entry, bool, error) {
	file, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, false, nil
		}
		return Entry{}, true, domain.Errorf(domain.CodeInternal,
			"无法读取预设缓存 %s", c.path).Wrap(err)
	}
	defer file.Close()
	data, err := ReadLimited(file, maxCacheBytes)
	if err != nil {
		return Entry{}, true, err
	}

	var wrapper cached
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return Entry{}, true, domain.Errorf(domain.CodeProtocolInvalid,
			"预设缓存内容无法解析").Wrap(err)
	}
	if len(wrapper.Payload) == 0 {
		return Entry{}, true, domain.Errorf(domain.CodeProtocolInvalid,
			"预设缓存里没有 payload")
	}

	catalogue, err := Parse(wrapper.Payload)
	if err != nil {
		return Entry{}, true, err
	}
	return Entry{
		Catalogue: catalogue,
		SourceURL: wrapper.SourceURL,
		CachedAt:  time.Unix(wrapper.CachedAt, 0).UTC(),
		Raw:       append([]byte(nil), wrapper.Payload...),
	}, true, nil
}

// Save validates the actual bytes before replacing the previous cache. A
// separately supplied Catalogue is not proof that these bytes were parsed.
func (c *Cache) Save(raw []byte, catalogue Catalogue, sourceURL string,
	at time.Time) error {

	if len(raw) == 0 {
		return domain.Errorf(domain.CodeInvalidArgument,
			"不写入空的预设缓存")
	}
	if catalogue.SchemaVersion != SchemaVersion {
		// The caller passed a catalogue it did not parse from these bytes.
		return domain.Errorf(domain.CodeInternal,
			"预设缓存只接受已经解析过的内容")
	}
	if _, err := Parse(raw); err != nil {
		return err
	}
	if len(sourceURL) > maxSourceBytes {
		return domain.Errorf(domain.CodeInvalidArgument, "预设来源地址过长")
	}

	metadata, err := json.Marshal(cached{
		CachedAt:  at.Unix(),
		SourceURL: sourceURL,
	})
	if err != nil {
		return domain.Errorf(domain.CodeInternal, "无法编码预设缓存").Wrap(err)
	}
	// MarshalIndent can amplify a deeply nested, otherwise bounded document.
	// Append validated JSON verbatim to keep both layout and allocations bounded.
	document := make([]byte, 0, len(metadata)+len(raw)+16)
	document = append(document, metadata[:len(metadata)-1]...)
	document = append(document, `,"payload":`...)
	document = append(document, raw...)
	document = append(document, '}', '\n')
	return writeAtomic(c.path, document)
}

// Clear removes the cache. Absent is success: that is the state it asks for.
func (c *Cache) Clear() error {
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return domain.Errorf(domain.CodeInternal,
			"无法删除预设缓存 %s", c.path).Wrap(err)
	}
	return nil
}

// writeAtomic writes a file that a reader never sees half of.
//
// Temp, fsync, rename, sync the directory. This helper also serves persistent
// user presets; cache callers use tmpfs and rely on the built-in after reboot.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法创建目录 %s", dir).Wrap(err)
	}

	temp, err := os.CreateTemp(dir, ".presets-*")
	if err != nil {
		return domain.Errorf(domain.CodeInternal,
			"无法在 %s 创建临时文件", dir).Wrap(err)
	}
	name := temp.Name()
	committed := false
	defer func() {
		if !committed {
			temp.Close()
			os.Remove(name)
		}
	}()

	if err := temp.Chmod(FileMode); err != nil {
		return domain.Errorf(domain.CodeInternal, "无法设置文件权限").Wrap(err)
	}
	if _, err := temp.Write(data); err != nil {
		return domain.Errorf(domain.CodeInternal, "写入失败").Wrap(err)
	}
	if err := temp.Sync(); err != nil {
		return domain.Errorf(domain.CodeInternal, "未能写入磁盘").Wrap(err)
	}
	if err := temp.Close(); err != nil {
		return domain.Errorf(domain.CodeInternal, "未能写入磁盘").Wrap(err)
	}
	if err := os.Rename(name, path); err != nil {
		return domain.Errorf(domain.CodeInternal, "提交失败").Wrap(err)
	}
	committed = true
	return syncDir(dir)
}
