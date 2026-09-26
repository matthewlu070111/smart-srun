package daemon

import (
	"context"
	"os"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/auth"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/policy"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
	"github.com/matthewlu070111/smart-srun/core/internal/wireless"
)

// This file is the assembly the rest of the program is arranged to avoid doing.
//
// application declares what it needs -- a Binder, Lines, Settings -- and cannot
// reach the adapter or the transport that satisfy them. That is what lets it be
// tested without a router. Somebody still has to put the two halves together,
// and this is the one place allowed to know about both.

// deviceBinder answers where a line is by asking the router.
type deviceBinder struct{ adapter *openwrt.Adapter }

func (b deviceBinder) ResolveBinding(ctx context.Context, iface string,
	generation uint64) (domain.Binding, error) {

	return b.adapter.ResolveBinding(ctx, iface, generation)
}

// LinkState replaces this program's word for a failure with the router's.
//
// "BindingUnavailable" is accurate and useless. Whether the interface is
// missing, the cable is out, or DHCP has not answered yet are three different
// things for the person who has to fix it, and only netifd can tell them apart.
func (b deviceBinder) LinkState(ctx context.Context, iface string) (domain.LinkState, error) {
	status, err := b.adapter.InterfaceStatus(ctx, iface)
	if err != nil {
		return domain.LinkMissing, err
	}
	return status.LinkState(), nil
}

// pooledLines hands out bound clients from the one connection pool.
type pooledLines struct{ pool *transport.Pool }

func (l pooledLines) Line(accountID string, binding domain.Binding,
	gateway string) (auth.Line, error) {

	client, err := l.pool.Get(accountID, binding, gateway)
	if err != nil {
		// Returned explicitly rather than as `return client, err`. A typed nil
		// pointer in an interface is not a nil interface, so the caller's check
		// for a missing line would be false on exactly the path that has none.
		return nil, err
	}
	return client, nil
}

func (l pooledLines) Retire(accountID string, generation uint64) int {
	return l.pool.Retire(accountID, generation)
}

// newDeviceRunner assembles the worker that actually authenticates.
//
// Wireless is supplied when a store could be built for it, and left nil when
// one could not. Nil is not a degraded mode that half-works: a switch is
// refused with UnsupportedCapability rather than half-performed, which is the
// same choice M08 made about actions with no worker behind them, and it is what
// a build running somewhere with no writable runtime directory should do.
func newDeviceRunner(settings application.Settings, pool *transport.Pool,
	clock policy.Clock, radio application.Wireless) application.Runner {

	return application.NewAuthenticator(application.AuthenticatorOptions{
		Binder:   deviceBinder{adapter: openwrt.NewAdapter(openwrt.Runner{})},
		Lines:    pooledLines{pool: pool},
		Settings: settings,
		Clock:    clock,
		Wireless: radio,
	})
}

// newDeviceWirelessFor builds the wireless half, or explains why it could not.
//
// The adapter and the store share one Runner so that the timeouts and the
// search path are decided once. The staging directory is created here rather
// than lazily: a change that discovered it could not stage half way through
// would have already refused somebody's switch for a reason that had nothing to
// do with wireless.
func newDeviceWirelessFor(paths Paths, settings application.Settings,
	clock policy.Clock) (*deviceWireless, error) {

	runner := openwrt.Runner{}
	store, err := wireless.NewUCIStore(runner, wireless.StoreOptions{
		Staging: paths.WirelessStaging(),
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.WirelessStaging(), wireless.DirMode); err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法创建无线暂存目录 %s", paths.WirelessStaging()).Wrap(err)
	}
	if err := os.MkdirAll(paths.Recovery(), wireless.DirMode); err != nil {
		return nil, domain.Errorf(domain.CodeInternal,
			"无法创建恢复目录 %s", paths.Recovery()).Wrap(err)
	}

	return newDeviceWireless(wirelessOptions{
		Adapter:  openwrt.NewAdapter(runner),
		Store:    store,
		Paths:    wireless.Paths{Dir: paths.Recovery()},
		Settings: settings,
		Clock:    clock,
	}), nil
}
