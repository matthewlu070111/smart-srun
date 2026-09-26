package openwrt

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// IPv4Address is one address netifd reports on an interface, with the prefix
// length it reports alongside it.
type IPv4Address struct {
	Address      netip.Addr
	PrefixLength int
}

// InterfaceError is netifd's own explanation for an interface that is not
// running: NO_DEVICE for one whose device is absent, and so on. It is kept
// because "the cable is unplugged" and "you named an interface that does not
// exist" look identical from the outside and need different advice.
type InterfaceError struct {
	Subsystem string `json:"subsystem"`
	Code      string `json:"code"`
}

// InterfaceStatus is what `ubus call network.interface.<name> status` said.
type InterfaceStatus struct {
	Name      string
	Up        bool
	Pending   bool
	Available bool
	Autostart bool
	Proto     string
	// Device is the configured device. L3Device is the one that actually
	// carries layer 3, and for PPPoE or a wireless uplink they differ; binding
	// to the configured one then sends nothing.
	Device     string
	L3Device   string
	IPv4       []IPv4Address
	DNSServers []netip.Addr
	Gateways   []netip.Addr
	Errors     []InterfaceError
}

// interfaceStatusJSON mirrors only the fields this program uses.
//
// Unknown fields are ignored, which is the opposite of the rule for the
// configuration file, and deliberately so: that document is ours and an
// unrecognised key means a mistake, while this one belongs to netifd and gains
// fields between firmware versions. Refusing to read a status because a new
// release added a counter would take the router offline for no reason.
type interfaceStatusJSON struct {
	Up        bool   `json:"up"`
	Pending   bool   `json:"pending"`
	Available bool   `json:"available"`
	Autostart bool   `json:"autostart"`
	Proto     string `json:"proto"`
	Device    string `json:"device"`
	L3Device  string `json:"l3_device"`

	IPv4Hyphen []struct {
		Address string `json:"address"`
		Mask    int    `json:"mask"`
	} `json:"ipv4-address"`
	// The underscore spelling is carried over from the baseline, which accepted
	// both. Only the hyphenated form has been seen on a real device; this costs
	// nothing and covers a firmware that spells it the other way.
	IPv4Underscore []struct {
		Address string `json:"address"`
		Mask    int    `json:"mask"`
	} `json:"ipv4_address"`

	DNSHyphen     []string `json:"dns-server"`
	DNSUnderscore []string `json:"dns_server"`
	Routes        []struct {
		Target  string `json:"target"`
		Mask    int    `json:"mask"`
		NextHop string `json:"nexthop"`
	} `json:"route"`

	Errors []InterfaceError `json:"errors"`
}

// ParseInterfaceStatus reads one interface's ubus status.
//
// Addresses that do not parse, or that are not IPv4, are dropped rather than
// reported: what matters is that they never become the address a socket binds
// to. An interface left with no usable address then fails as
// BindingUnavailable, which is the honest outcome.
func ParseInterfaceStatus(name string, data []byte) (InterfaceStatus, error) {
	if len(data) == 0 {
		return InterfaceStatus{}, domain.Errorf(domain.CodeInternal,
			"接口 %s 的状态查询没有返回内容", name)
	}
	var raw interfaceStatusJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return InterfaceStatus{}, domain.Errorf(domain.CodeInternal,
			"无法解析接口 %s 的状态", name).Wrap(err)
	}

	status := InterfaceStatus{
		Name:      name,
		Up:        raw.Up,
		Pending:   raw.Pending,
		Available: raw.Available,
		Autostart: raw.Autostart,
		Proto:     raw.Proto,
		Errors:    raw.Errors,
	}
	if device, ok := NormalizeDeviceName(raw.Device); ok {
		status.Device = device
	}
	if device, ok := NormalizeDeviceName(raw.L3Device); ok {
		status.L3Device = device
	}

	addresses := raw.IPv4Hyphen
	if len(addresses) == 0 {
		addresses = raw.IPv4Underscore
	}
	for _, entry := range addresses {
		address, err := netip.ParseAddr(strings.TrimSpace(entry.Address))
		// Is4 excludes an IPv6 address that turned up in the v4 list, and also
		// the ::ffff: form, which is not something to bind a v4 socket to.
		if err != nil || !address.Is4() {
			continue
		}
		status.IPv4 = append(status.IPv4, IPv4Address{
			Address: address, PrefixLength: entry.Mask,
		})
	}

	servers := raw.DNSHyphen
	if len(servers) == 0 {
		servers = raw.DNSUnderscore
	}
	for _, entry := range servers {
		server, err := netip.ParseAddr(strings.TrimSpace(entry))
		if err != nil {
			continue
		}
		status.DNSServers = append(status.DNSServers, server)
	}
	for _, route := range raw.Routes {
		gateway, err := netip.ParseAddr(route.NextHop)
		if route.Target == "0.0.0.0" && route.Mask == 0 && err == nil && gateway.Is4() && !gateway.IsUnspecified() && !gateway.IsLoopback() && !gateway.IsMulticast() {
			status.Gateways = append(status.Gateways, gateway)
		}
	}
	return status, nil
}

// LinkState collapses the status into the dimension spec 04 defines.
func (s InterfaceStatus) LinkState() domain.LinkState {
	switch {
	case !s.Available && s.L3Device == "" && s.Device == "":
		return domain.LinkMissing
	case !s.Up:
		return domain.LinkDown
	case s.EffectiveDevice() == "":
		return domain.LinkMissing
	case len(s.IPv4) == 0:
		return domain.LinkAddressPending
	default:
		return domain.LinkReady
	}
}

// EffectiveDevice is the device to bind to: the layer 3 one when netifd knows
// it, otherwise the configured one.
func (s InterfaceStatus) EffectiveDevice() string {
	if s.L3Device != "" {
		return s.L3Device
	}
	return s.Device
}

// ErrorCodes lists netifd's own error codes, for a diagnosis the user can act
// on rather than a generic failure.
func (s InterfaceStatus) ErrorCodes() []string {
	out := make([]string, 0, len(s.Errors))
	for _, item := range s.Errors {
		if item.Code != "" {
			out = append(out, item.Code)
		}
	}
	return out
}

// NormalizeDeviceName cleans and validates a Linux network device name.
//
// `ip link` prints a VLAN as "eth0.2@eth0" and appends a colon to the name, so
// both are trimmed. The character set and the 15-byte limit are the kernel's
// (IFNAMSIZ includes the terminator): a name outside them cannot be a device,
// and letting it through would put an arbitrary string into an argv or a
// setsockopt.
//
// It is idempotent, and that is not decoration. Trimming a single trailing
// colon turned "::" into ":", which then passed the character check and came
// back as a device name -- while normalising that answer a second time
// rejected it. Any caller that re-validated got a different result from the
// one that first accepted the value. Found by fuzzing; the corpus entry is
// kept.
func NormalizeDeviceName(raw string) (string, bool) {
	device, _, _ := strings.Cut(strings.TrimSpace(raw), "@")
	device = strings.TrimRight(device, ":")
	if device == "" || len(device) > 15 {
		return "", false
	}
	// A name made only of punctuation is not a device. Requiring one
	// alphanumeric character is what rejects ":", "..." and "---", none of
	// which the kernel would ever produce.
	if !strings.ContainsFunc(device, func(symbol rune) bool {
		return (symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9')
	}) {
		return "", false
	}
	for index := 0; index < len(device); index++ {
		symbol := device[index]
		valid := (symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9') ||
			symbol == '_' || symbol == '.' || symbol == ':' || symbol == '-'
		if !valid {
			return "", false
		}
	}
	// "." and ".." would be read as path components by anything that joins the
	// name onto /sys/class/net.
	if device == "." || device == ".." {
		return "", false
	}
	return device, true
}

// IsLogicalInterfaceName reports whether a name can be an OpenWrt interface.
//
// These come from uci section names, which allow only letters, digits and
// underscores -- no dots. That is what separates "wan", a netifd interface to
// ask ubus about, from "wan.v2", which can only be a Linux device.
func IsLogicalInterfaceName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for index := 0; index < len(name); index++ {
		symbol := name[index]
		valid := (symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9') || symbol == '_'
		if !valid {
			return false
		}
	}
	return true
}
