package openwrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ubusStatusNotFound is UBUS_STATUS_NOT_FOUND, which the ubus CLI returns as
// its exit status when the object does not exist. Asking for an interface that
// is not configured is an ordinary mistake -- the user typed a name, or deleted
// the interface after selecting it -- and deserves that answer rather than a
// generic failure.
const ubusStatusNotFound = 4

// commandRunner is the part of Runner this adapter uses.
//
// Declared here, by the consumer, rather than beside Runner: it exists so a
// test can drive the composition -- which tool, which arguments, how a failure
// is reported -- without a router. The guarantees about running a process are
// Runner's own and are tested against real processes, not through this.
type commandRunner interface {
	Run(ctx context.Context, program string, args ...string) (Result, error)
	Resolve(program string) (string, error)
}

// Adapter is this program's view of the router.
//
// It owns the tools it runs and the way their answers become values. One
// instance is shared: there is no per-call construction and no global, so the
// timeouts and the search path are decided once, where the daemon is wired up.
type Adapter struct {
	runner commandRunner

	// deviceFacts reads a device's index and addresses straight from the
	// kernel. It is a field so a test can present a device that does not exist
	// on the machine running the test; the default reads netlink through the
	// standard library, with no subprocess and nothing to parse.
	deviceFacts func(device string) (int, []netip.Addr, error)
}

// NewAdapter builds an adapter over a runner.
func NewAdapter(runner commandRunner) *Adapter {
	return &Adapter{runner: runner, deviceFacts: kernelDeviceFacts}
}

// InterfaceStatus asks netifd about one logical interface.
func (a *Adapter) InterfaceStatus(ctx context.Context, name string) (InterfaceStatus, error) {
	if !IsLogicalInterfaceName(name) {
		return InterfaceStatus{}, domain.FieldErrorf(domain.CodeInvalidArgument,
			"wired_iface", "%q 不是有效的接口名", name)
	}
	// The object name is built from a name already checked against the
	// character set uci allows, so nothing a user typed can turn into another
	// ubus object or another argument.
	result, err := a.runner.Run(ctx, "ubus", "call", "network.interface."+name, "status")
	if err != nil {
		var exit *ExitError
		if errors.As(err, &exit) && exit.Code == ubusStatusNotFound {
			return InterfaceStatus{}, domain.FieldErrorf(domain.CodeNotFound,
				"wired_iface", "系统中没有名为 %s 的网络接口", name).Wrap(err)
		}
		return InterfaceStatus{}, err
	}
	if result.StdoutTruncated {
		return InterfaceStatus{}, domain.Errorf(domain.CodeInternal,
			"接口 %s 的状态过长，已截断", name)
	}
	return ParseInterfaceStatus(name, result.Stdout)
}

// UCI reads one uci package.
func (a *Adapter) UCI(ctx context.Context, pkg string) (UCIConfig, error) {
	if !IsLogicalInterfaceName(pkg) {
		return UCIConfig{}, domain.Errorf(domain.CodeInvalidArgument,
			"%q 不是有效的配置名", pkg)
	}
	result, err := a.runner.Run(ctx, "uci", "show", pkg)
	if err != nil {
		return UCIConfig{}, missingPackage(pkg, err)
	}
	if result.StdoutTruncated {
		return UCIConfig{}, domain.Errorf(domain.CodeInternal,
			"配置 %s 过长，已截断", pkg)
	}
	return ParseUCIShow(pkg, result.Stdout)
}

// UCIExport retains anonymous section names and singleton list types.
func (a *Adapter) UCIExport(ctx context.Context, pkg string) (UCIConfig, error) {
	if !IsLogicalInterfaceName(pkg) {
		return UCIConfig{}, domain.Errorf(domain.CodeInvalidArgument, "配置包名无效")
	}
	result, err := a.runner.Run(ctx, "uci", "-n", "export", pkg)
	if err != nil {
		return UCIConfig{}, err
	}
	if result.StdoutTruncated {
		return UCIConfig{}, domain.Errorf(domain.CodeInternal, "UCI 配置过长")
	}
	return ParseUCIExport(pkg, result.Stdout)
}

// missingPackage names the one failure `uci show <package>` actually has.
//
// A router with no wireless hardware has no /etc/config/wireless at all, and
// uci answers "Entry not found" with status 1. That is not a fault to retry: it
// is a system with nothing to configure, and it needs a different answer from
// "the uci command failed". Observed on OpenWrt 24.10.8, which ships no
// wireless configuration when there is no radio.
func missingPackage(pkg string, err error) error {
	var exit *ExitError
	if errors.As(err, &exit) && exit.Code == 1 {
		return domain.Errorf(domain.CodeNotFound,
			"系统上没有 %s 配置", pkg).Wrap(err)
	}
	return err
}

// PendingChanges lists the uncommitted changes in a uci package.
//
// Spec 04 starts the wireless transaction by confirming there are none:
// committing on top of somebody else's staged edit publishes a change they
// never applied, under this program's name.
func (a *Adapter) PendingChanges(ctx context.Context, pkg string) ([]UCIChange, error) {
	if !IsLogicalInterfaceName(pkg) {
		return nil, domain.Errorf(domain.CodeInvalidArgument,
			"%q 不是有效的配置名", pkg)
	}
	result, err := a.runner.Run(ctx, "uci", "changes", pkg)
	if err != nil {
		return nil, missingPackage(pkg, err)
	}
	if result.StdoutTruncated {
		return nil, domain.Errorf(domain.CodeInternal,
			"配置 %s 的未提交改动过多，无法完整读取", pkg)
	}
	return ParseUCIChanges(result.Stdout)
}

// ResolveBinding turns the interface the user selected into the facts needed to
// send an authentication request from it.
//
// Resolution is forward only, from the selected interface to its device and
// address. Spec 04 forbids the reverse -- looking up which interface owns an
// address -- because two campus lines routinely hand out overlapping private
// subnets, so an address maps back to whichever interface is checked first, and
// the credentials then leave through the wrong one.
//
// The generation is the caller's, not this function's: it identifies the
// observation for the transport pool that will cache connections under it, and
// making it a parameter means it is never an accidental zero.
func (a *Adapter) ResolveBinding(ctx context.Context, logicalIface string,
	generation uint64) (domain.Binding, error) {

	if logicalIface == "" {
		return domain.Binding{}, domain.FieldErrorf(domain.CodeInvalidArgument,
			"wired_iface", "未选择有线接口")
	}

	binding := domain.Binding{LogicalIface: logicalIface, Generation: generation}
	var (
		device   string
		expected netip.Addr
	)

	if IsLogicalInterfaceName(logicalIface) {
		status, err := a.InterfaceStatus(ctx, logicalIface)
		if code, ok := domain.CodeOf(err); ok && code == domain.CodeNotFound {
			// The interface was deleted after being selected, or the name was
			// typed wrong. That is a setting to correct, not a line to wait
			// for, and the message already says which -- so it is passed
			// through rather than flattened into "cannot read interface".
			return domain.Binding{}, err
		}
		if err != nil {
			return domain.Binding{}, unavailable(logicalIface,
				"无法读取接口状态").Wrap(err)
		}
		if state := status.LinkState(); state != domain.LinkReady {
			return domain.Binding{}, linkProblem(logicalIface, status)
		}
		device = status.EffectiveDevice()
		expected = status.IPv4[0].Address
		binding.DNSServers = status.DNSServers
	} else if normalized, ok := NormalizeDeviceName(logicalIface); ok {
		// Not a uci section name, so it cannot be a netifd interface. A literal
		// Linux device such as "wan.v2" is still a legitimate selection, and
		// its address comes straight from the kernel.
		device = normalized
	} else {
		return domain.Binding{}, domain.FieldErrorf(domain.CodeInvalidArgument,
			"wired_iface", "%q 既不是接口名也不是设备名", logicalIface)
	}

	index, addresses, err := a.deviceFacts(device)
	if err != nil {
		return domain.Binding{}, unavailable(logicalIface,
			"设备 %s 不存在或无法读取", device).Wrap(err)
	}

	source, err := chooseSourceAddress(logicalIface, device, expected, addresses)
	if err != nil {
		return domain.Binding{}, err
	}

	binding.L3Device = device
	binding.IfIndex = index
	binding.SourceIPv4 = source
	return binding, nil
}

// chooseSourceAddress settles which address the socket binds to, and refuses
// when netifd and the kernel disagree.
//
// The cross-check is spec 04's second binding rule. netifd's status is a cache
// that lags a DHCP change, and two interfaces on overlapping subnets can report
// the same address; binding to an address the device does not actually hold
// either fails outright or, worse, succeeds through the default route and
// authenticates the wrong line.
func chooseSourceAddress(logicalIface, device string, expected netip.Addr,
	addresses []netip.Addr) (netip.Addr, error) {

	usable := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if address.Is4() && !address.IsUnspecified() {
			usable = append(usable, address)
		}
	}
	if len(usable) == 0 {
		return netip.Addr{}, unavailable(logicalIface,
			"设备 %s 还没有 IPv4 地址", device)
	}
	if !expected.IsValid() {
		// The device path: the selection was a Linux device name, so there is
		// no netifd interface to say which of its addresses is the one. The
		// first is taken, in the kernel's order, which is what the baseline
		// did. A device with a secondary address would need the gateway to
		// disambiguate, and that is not known here.
		return usable[0], nil
	}
	for _, address := range usable {
		if address == expected {
			return address, nil
		}
	}
	return netip.Addr{}, unavailable(logicalIface,
		"接口报告的地址不在设备 %s 上，线路可能刚刚变化", device)
}

// unavailable builds the field error for a line that cannot carry a request.
//
// The interface name is an argument rather than part of the format string: it
// comes from the user's configuration, and splicing it into a format would make
// a name containing a percent sign produce a mangled message. The validators
// happen to reject one today, which is exactly the kind of thing that stops
// being true later.
func unavailable(iface, detail string, args ...any) *domain.Error {
	return domain.FieldErrorf(domain.CodeBindingUnavailable, "wired_iface",
		"有线接口 %s：%s", iface, fmt.Sprintf(detail, args...))
}

// linkProblem turns a link state into advice, because the four reasons an
// interface is not ready need four different things from the user.
func linkProblem(iface string, status InterfaceStatus) error {
	switch status.LinkState() {
	case domain.LinkMissing:
		codes := status.ErrorCodes()
		if len(codes) > 0 {
			return unavailable(iface, "接口没有可用设备（%s）", codes[0])
		}
		return unavailable(iface, "接口没有可用设备")
	case domain.LinkDown:
		return unavailable(iface, "接口未启用，请检查网线或上游连接")
	case domain.LinkAddressPending:
		return unavailable(iface, "接口尚未获取到 IPv4 地址")
	default:
		return unavailable(iface, "接口尚未就绪")
	}
}

// kernelDeviceFacts reads a device's index and addresses from netlink.
//
// The standard library asks the kernel the same question `ip addr` does,
// without a process to start, output to parse, or a tool that might be absent.
func kernelDeviceFacts(device string) (int, []netip.Addr, error) {
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return 0, nil, err
	}
	raw, err := iface.Addrs()
	if err != nil {
		return 0, nil, err
	}
	addresses := make([]netip.Addr, 0, len(raw))
	for _, entry := range raw {
		prefix, ok := entry.(*net.IPNet)
		if !ok {
			continue
		}
		address, ok := netip.AddrFromSlice(prefix.IP)
		if !ok {
			continue
		}
		addresses = append(addresses, address.Unmap())
	}
	return iface.Index, addresses, nil
}
