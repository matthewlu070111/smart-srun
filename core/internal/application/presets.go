package application

import (
	"context"
	"errors"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/presets"
)

const PresetRefreshBudget = 35 * time.Second

// PresetRefresher owns one bounded refresh. The coordinator serializes these
// workers because all lines publish to the same catalogue cache.
type PresetRefresher struct {
	Resolve func(context.Context, string, uint64) (domain.Binding, error)
	Open    func(domain.Binding) (presets.Fetcher, func(), error)
	Cache   *presets.Cache
	Sources []string
	Now     func() time.Time
}

func (w PresetRefresher) Run(parent context.Context, action Action, report func(Phase)) Outcome {
	ctx, cancel := context.WithTimeout(parent, PresetRefreshBudget)
	defer cancel()
	fail := func(err error) Outcome {
		code, ok := domain.CodeOf(err)
		if !ok {
			code = domain.CodeInternal
		}
		if errors.Is(err, context.Canceled) {
			code = domain.CodeCancelled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = domain.CodeDeadlineExceeded
		}
		return Outcome{State: StateFailed, Code: code, Message: "预设刷新失败；仍可使用本地目录"}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if w.Resolve == nil || w.Open == nil || w.Cache == nil {
		return fail(domain.Errorf(domain.CodeInternal, "预设刷新依赖未装配"))
	}
	binding, err := w.Resolve(ctx, action.Request.Interface, action.Sequence)
	if err != nil {
		return fail(err)
	}
	if !binding.Ready() || binding.LogicalIface != action.Request.Interface || binding.Generation != action.Sequence {
		return fail(domain.Errorf(domain.CodeBindingUnavailable, "指定线路未就绪"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	fetcher, closeClient, err := w.Open(binding)
	if err != nil {
		return fail(err)
	}
	if fetcher == nil || closeClient == nil {
		if closeClient != nil {
			closeClient()
		}
		return fail(domain.Errorf(domain.CodeInternal, "预设客户端装配不完整"))
	}
	defer closeClient()
	report(PhaseFetch)
	result, err := presets.Refresh(ctx, fetcher, w.Sources, w.Cache, w.Now)
	if err != nil {
		return fail(err)
	}
	message := "预设目录已刷新"
	if !result.Replaced {
		message = "已检查预设来源，保留较新的本地缓存"
	}
	return Outcome{State: StateSucceeded, Message: message}
}
