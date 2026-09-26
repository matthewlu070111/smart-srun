package presets

import (
	"context"
	"os"
	"strconv"
	"sync"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const emptyUsers = "{\"schema_version\":2,\"revision\":0,\"presets\":[],\"operators\":[]}\n"

// UserStore is the daemon's sole writer of user-presets.json. The daemon's
// process lock excludes other processes; this mutex serializes its RPC callers.
// Its revision is independent of config.json and of the remote cache.
type UserStore struct {
	path string
	mu   sync.Mutex
}

func NewUserStore(path string) *UserStore { return &UserStore{path: path} }

func (s *UserStore) Get() (UserDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *UserStore) load() (UserDocument, error) {
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return ParseUsers([]byte(emptyUsers))
	}
	if err != nil {
		return UserDocument{}, domain.Errorf(domain.CodeInternal, "无法读取用户预设").Wrap(err)
	}
	defer file.Close()
	data, err := ReadLimited(file, MaxUserBytes)
	if err != nil {
		return UserDocument{}, err
	}
	return ParseUsers(data)
}

// Set replaces a document after a compare-and-set against this file alone.
// public must be the current merged public catalogue, including drafts, so a
// custom preset cannot silently shadow a public entry. Neither catalogue nor
// account settings are modified by this operation.
func (s *UserStore) Set(ctx context.Context, expected uint64, data []byte,
	public []School) (UserDocument, error) {

	if err := ctx.Err(); err != nil {
		return UserDocument{}, err
	}
	next, err := ParseUsers(data)
	if err != nil {
		return UserDocument{}, err
	}
	publicIDs := make(map[string]bool, len(public))
	for _, school := range public {
		publicIDs[SafeID(school.ShortName)] = true
	}
	for _, preset := range next.Presets {
		if publicIDs[SafeID(preset.School.ShortName)] {
			return UserDocument{}, userError("自定义预设标识与公共学校预设冲突")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.load()
	if err != nil {
		return UserDocument{}, err
	}
	if expected != current.Revision || next.Revision != expected {
		return UserDocument{}, domain.Errorf(domain.CodeConflict,
			"用户预设已变化，请重新读取后再保存（当前版本 %d）", current.Revision)
	}
	if current.Revision == ^uint64(0) {
		return UserDocument{}, userError("revision 已达到上限")
	}
	// Replace only the revision token, preserving the caller's source text.
	raw := append([]byte(nil), next.raw[:next.revision.start]...)
	raw = strconv.AppendUint(raw, current.Revision+1, 10)
	raw = append(raw, next.raw[next.revision.end:]...)
	next, err = ParseUsers(raw)
	if err != nil {
		return UserDocument{}, err
	}
	if err := ctx.Err(); err != nil {
		return UserDocument{}, err
	}
	if err := writeAtomic(s.path, raw); err != nil {
		// Reads always load the file. If rename succeeded but directory sync
		// failed, the next CAS sees the new on-disk revision, not a stale copy.
		return UserDocument{}, err
	}
	return next, nil
}
