package openwrt

import (
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestDiscoveryGatewaysComeOnlyFromIPv4DefaultRoutes(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", []byte(`{"route":[
		{"target":"0.0.0.0","mask":0,"nexthop":"10.0.2.2"},
		{"target":"10.0.0.0","mask":8,"nexthop":"10.0.2.3"},
		{"target":"0.0.0.0","mask":24,"nexthop":"10.0.2.4"},
		{"target":"::","mask":0,"nexthop":"fe80::1"},
		{"target":"0.0.0.0","mask":0,"nexthop":"127.0.0.1"},
		{"target":"0.0.0.0","mask":0,"nexthop":"0.0.0.0"},
		{"target":"0.0.0.0","mask":0,"nexthop":"invalid"}
	]}`))
	if err != nil || len(status.Gateways) != 1 || status.Gateways[0].String() != "10.0.2.2" {
		t.Fatalf("%+v / %v", status, err)
	}
}

// T12 -- the four link states, each from a real interface on a real router.
//
// These are not four hand-written JSON documents chosen to make the code look
// right. They were captured from one device at one moment: a wan whose cable is
// out, a wireless uplink carrying traffic, a tunnel that is up with no address,
// and an interface whose device does not exist.
func TestTheFourLinkStatesComeFromRealInterfaces(t *testing.T) {
	cases := []struct {
		fixture string
		name    string
		want    domain.LinkState
		reason  string
	}{
		{"kwrt-25.12/iface-wan-down.json", "wan", domain.LinkDown,
			"the interface exists and has a device, but is not up"},
		{"kwrt-25.12/iface-wwan-up.json", "wwan", domain.LinkReady,
			"up, with an L3 device and an address"},
		{"kwrt-25.12/iface-tunnel-no-address.json", "EasyTier", domain.LinkAddressPending,
			"up with a device, but no IPv4 yet"},
		{"kwrt-25.12/iface-no-device.json", "wan6", domain.LinkMissing,
			"netifd reports NO_DEVICE"},
		{"kwrt-25.12/iface-lan.json", "lan", domain.LinkReady,
			"a bridge, whose L3 device is br-lan and not the interface name"},
		// Two more firmware images, so the reader is not tuned to one build's
		// field set: a second Kwrt build, and official OpenWrt 24.10 on x86_64,
		// which is a different release generation entirely.
		{"kwrt-25.12-feb/iface-wan.json", "wan", domain.LinkReady,
			"the same shape from a different firmware build"},
		{"openwrt-24.10/iface-wan-up.json", "wan", domain.LinkReady,
			"official 24.10 on x86_64, a different release generation"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, err := ParseInterfaceStatus(testCase.name, testdata(t, testCase.fixture))
			if err != nil {
				t.Fatalf("ParseInterfaceStatus: %v", err)
			}
			if got := status.LinkState(); got != testCase.want {
				t.Errorf("state = %s, want %s (%s)", got, testCase.want, testCase.reason)
			}
		})
	}
}

// The L3 device is not the interface name and not always the configured device.
// A wireless uplink called "wwan" is carried by phy1-sta0, and binding a socket
// to "wwan" reaches nothing.
func TestTheLayer3DeviceIsReadRatherThanAssumed(t *testing.T) {
	status, err := ParseInterfaceStatus("wwan", testdata(t, "kwrt-25.12/iface-wwan-up.json"))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}

	if status.L3Device != "phy1-sta0" {
		t.Errorf("L3Device = %q, want phy1-sta0", status.L3Device)
	}
	if status.EffectiveDevice() != "phy1-sta0" {
		t.Errorf("EffectiveDevice = %q", status.EffectiveDevice())
	}
	if len(status.IPv4) != 1 || !status.IPv4[0].Address.Is4() {
		t.Fatalf("addresses = %+v", status.IPv4)
	}
	if status.IPv4[0].PrefixLength != 15 {
		t.Errorf("prefix length = %d, want the one netifd reported",
			status.IPv4[0].PrefixLength)
	}
	if len(status.DNSServers) != 2 {
		t.Errorf("DNS servers = %v; the line's own resolvers are part of the "+
			"binding", status.DNSServers)
	}
}

// An interface that is down has no L3 device at all, only the configured one --
// so falling back to it is what lets the caller name the device in a diagnosis
// instead of saying nothing.
func TestADownInterfaceStillNamesItsConfiguredDevice(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", testdata(t, "kwrt-25.12/iface-wan-down.json"))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}
	if status.L3Device != "" {
		t.Errorf("L3Device = %q, want empty for a down interface", status.L3Device)
	}
	if status.EffectiveDevice() != "eth1" {
		t.Errorf("EffectiveDevice = %q, want eth1", status.EffectiveDevice())
	}
}

// netifd's own error code is what turns "not connected" into advice.
func TestNetifdsErrorCodeIsKept(t *testing.T) {
	status, err := ParseInterfaceStatus("wan6", testdata(t, "kwrt-25.12/iface-no-device.json"))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}
	codes := status.ErrorCodes()
	if len(codes) != 1 || codes[0] != "NO_DEVICE" {
		t.Errorf("codes = %v, want [NO_DEVICE]", codes)
	}
}

// T12 -- an address that is not a usable IPv4 must never become one.
//
// Dropping it leaves the interface with no address, which fails as
// BindingUnavailable. Keeping it would put a value into a bind() call that
// either fails obscurely or, if it happens to parse, binds to the wrong thing.
func TestAnAddressThatIsNotUsableIPv4IsDropped(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", []byte(`{
		"up": true, "available": true, "l3_device": "eth1", "proto": "dhcp",
		"ipv4-address": [
			{"address": "not-an-address", "mask": 24},
			{"address": "", "mask": 24},
			{"address": "2001:db8::1", "mask": 64},
			{"address": "::ffff:192.0.2.5", "mask": 24},
			{"address": "192.0.2.7", "mask": 24}
		],
		"dns-server": ["also-not-an-address", "192.0.2.53"]
	}`))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}

	if len(status.IPv4) != 1 {
		t.Fatalf("kept %d addresses, want only the valid one: %+v",
			len(status.IPv4), status.IPv4)
	}
	if status.IPv4[0].Address != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("address = %v", status.IPv4[0].Address)
	}
	if len(status.DNSServers) != 1 {
		t.Errorf("DNS servers = %v, want only the parseable one", status.DNSServers)
	}
}

// An interface reporting nothing usable is address-pending, so the caller waits
// instead of authenticating from whatever the default route offers.
func TestAnInterfaceWithOnlyUnusableAddressesIsNotReady(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", []byte(`{
		"up": true, "available": true, "l3_device": "eth1",
		"ipv4-address": [{"address": "2001:db8::1", "mask": 64}]
	}`))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}
	if got := status.LinkState(); got != domain.LinkAddressPending {
		t.Errorf("state = %s, want AddressPending", got)
	}
}

// ubus that answers with nothing, or with something that is not JSON, is a
// failure -- not an interface with no addresses.
func TestUnreadableStatusIsAnErrorNotAnEmptyInterface(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       "",
		"not json":    "Command failed: Not found",
		"a fragment":  `{"up": true`,
		"not object":  `[1, 2, 3]`,
		"just a word": `"up"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInterfaceStatus("wan", []byte(body)); err == nil {
				t.Error("unreadable output parsed successfully")
			}
		})
	}
}

// The underscore spelling is accepted because the baseline accepted it. Only
// the hyphenated form has been seen on a real device, so this records that the
// alternative is carried deliberately rather than by accident.
func TestTheUnderscoreSpellingIsAlsoAccepted(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", []byte(`{
		"up": true, "available": true, "l3_device": "eth1",
		"ipv4_address": [{"address": "192.0.2.9", "mask": 24}],
		"dns_server": ["192.0.2.53"]
	}`))
	if err != nil {
		t.Fatalf("ParseInterfaceStatus: %v", err)
	}
	if len(status.IPv4) != 1 || status.IPv4[0].Address.String() != "192.0.2.9" {
		t.Errorf("addresses = %+v", status.IPv4)
	}
	if len(status.DNSServers) != 1 {
		t.Errorf("DNS servers = %v", status.DNSServers)
	}
}

// A field netifd adds in a later release must not stop the router working.
// This is the opposite of the rule for the configuration file, where an unknown
// key is a mistake -- that document is ours and this one is not.
func TestAnUnknownFieldFromANewerFirmwareIsIgnored(t *testing.T) {
	status, err := ParseInterfaceStatus("wan", []byte(`{
		"up": true, "available": true, "l3_device": "eth1",
		"ipv4-address": [{"address": "192.0.2.9", "mask": 24}],
		"some_future_counter": 42,
		"nested": {"anything": [1, 2, {"deep": true}]}
	}`))
	if err != nil {
		t.Fatalf("a new field made the interface unreadable: %v", err)
	}
	if status.LinkState() != domain.LinkReady {
		t.Errorf("state = %s, want Ready", status.LinkState())
	}
}

// A device name goes into argv and into setsockopt. Anything outside the
// kernel's own character set and length is not a device.
func TestDeviceNamesAreCleanedAndBounded(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"eth0":             {"eth0", true},
		"wan.v2":           {"wan.v2", true},
		"phy1-sta0":        {"phy1-sta0", true},
		"br-lan":           {"br-lan", true},
		"eth0.2@eth0":      {"eth0.2", true},
		"eth0:":            {"eth0", true},
		"  eth0  ":         {"eth0", true},
		"":                 {"", false},
		"   ":              {"", false},
		"eth0 eth1":        {"", false},
		"eth0;reboot":      {"", false},
		"eth0\nwan":        {"", false},
		"$(id)":            {"", false},
		"../../etc/passwd": {"", false},
		".":                {"", false},
		"..":               {"", false},
		"0123456789abcdef": {"", false},
		"0123456789abcde":  {"0123456789abcde", true},
		"设备":               {"", false},
		"eth0\x00extra":    {"", false},
		"@eth0":            {"", false},
		"a/b":              {"", false},
		// Punctuation is not a device name. "::" used to normalise to ":",
		// which passed the character check and then failed when normalised
		// again -- found by fuzzing.
		"::":     {"", false},
		":":      {"", false},
		"...":    {"", false},
		"---":    {"", false},
		"eth0::": {"eth0", true},
		"eth0:1": {"eth0:1", true},
	}
	for input, want := range cases {
		got, ok := NormalizeDeviceName(input)
		if ok != want.ok || got != want.want {
			t.Errorf("NormalizeDeviceName(%q) = (%q, %v), want (%q, %v)",
				input, got, ok, want.want, want.ok)
		}
	}
}

// Normalising twice must give the same answer.
//
// A caller that re-validates a name it was already given has to get the same
// verdict, or a value can be accepted at one layer and refused at the next for
// no visible reason. This is what "::" broke.
func TestNormalisingADeviceNameIsIdempotent(t *testing.T) {
	inputs := []string{
		"eth0", "eth0.2@eth0", "eth0:", "eth0::", "::", ":", "...",
		"br-lan", "phy1-sta0", "wan.v2", "", "  eth0  ", "eth0:1",
	}
	for _, input := range inputs {
		once, okOnce := NormalizeDeviceName(input)
		if !okOnce {
			continue
		}
		twice, okTwice := NormalizeDeviceName(once)
		if !okTwice || twice != once {
			t.Errorf("NormalizeDeviceName(%q) = %q, but normalising that gives "+
				"(%q, %v)", input, once, twice, okTwice)
		}
	}
}

// The dot is what separates a netifd interface from a Linux device: uci section
// names cannot contain one, so "wan.v2" can only be a device.
func TestALogicalInterfaceNameIsAUCISectionName(t *testing.T) {
	cases := map[string]bool{
		"wan":       true,
		"wan6":      true,
		"EasyTier":  true,
		"my_wan_2":  true,
		"wan.v2":    false,
		"br-lan":    false,
		"":          false,
		"wan wan2":  false,
		"wan;uci":   false,
		"network.@": false,
	}
	for name, want := range cases {
		if got := IsLogicalInterfaceName(name); got != want {
			t.Errorf("IsLogicalInterfaceName(%q) = %v, want %v", name, got, want)
		}
	}
}

// Spec 04 refuses to accept a resolver on this machine as proof that a name was
// resolved through the selected line.
func TestLoopbackOnlyResolversAreRecognised(t *testing.T) {
	cases := []struct {
		name    string
		servers []string
		want    bool
	}{
		{"only the local resolver", []string{"127.0.0.1"}, true},
		{"two local resolvers", []string{"127.0.0.1", "127.0.0.53"}, true},
		{"one local and one on the line", []string{"127.0.0.1", "192.0.2.53"}, false},
		{"resolvers on the line", []string{"192.0.2.53"}, false},
		{"none at all", nil, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			binding := domain.Binding{}
			for _, server := range testCase.servers {
				binding.DNSServers = append(binding.DNSServers,
					netip.MustParseAddr(server))
			}
			if got := binding.DNSIsLoopbackOnly(); got != testCase.want {
				t.Errorf("DNSIsLoopbackOnly() = %v, want %v", got, testCase.want)
			}
		})
	}
}
