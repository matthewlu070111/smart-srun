package domain

import (
	"net/netip"
	"slices"
)

// LinkState is the first of the three independent state dimensions spec 04
// requires. It is deliberately not a bool: "not online" has four causes that
// need four different answers from the interface, and collapsing them is how
// a user with an unplugged cable gets told their password is wrong.
type LinkState string

const (
	// LinkMissing -- the interface does not exist, or netifd reports no device
	// for it. Nothing will improve without a configuration change.
	LinkMissing LinkState = "Missing"
	// LinkDown -- the interface exists but is not up.
	LinkDown LinkState = "LinkDown"
	// LinkAddressPending -- up, with an L3 device, but no IPv4 yet. Usually
	// DHCP still in flight, and usually worth waiting for.
	LinkAddressPending LinkState = "AddressPending"
	// LinkReady -- an L3 device and an address that belongs to it.
	LinkReady LinkState = "Ready"
)

// AuthState is the second of the three dimensions spec 04 requires.
//
// It is kept apart from the link and connectivity states because the three
// answer different questions and a single "online" bool cannot. The gateway
// accepting a login does not mean the session belongs to this account, and an
// account being online does not mean the internet is reachable.
type AuthState string

const (
	// AuthUnknown -- nothing has been asked yet.
	AuthUnknown AuthState = "Unknown"
	// AuthAuthenticating -- a request is in flight. Its outcome is not known,
	// and spec 04 is explicit that a cancellation here does not mean the
	// gateway did not receive it.
	AuthAuthenticating AuthState = "Authenticating"
	// AuthAccepted -- the gateway said the login succeeded. That is the
	// gateway's claim, not yet a verified identity.
	AuthAccepted AuthState = "Accepted"
	// AuthVerifiedSelf -- a query confirmed the online session belongs to this
	// account. This is the only state that means what a user reads as "logged
	// in".
	AuthVerifiedSelf AuthState = "VerifiedSelf"
	// AuthVerifiedOther -- somebody is online on this line, and it is not this
	// account. Reported, never acted on automatically: spec 04 forbids
	// knocking another identity off without an explicit manual action.
	AuthVerifiedOther AuthState = "VerifiedOther"
	// AuthRejected -- the gateway refused. A wrong password lives here, and it
	// must not be retried in a loop.
	AuthRejected AuthState = "Rejected"
	// AuthOffline -- a query confirmed that nobody is authenticated on this
	// line.
	//
	// Distinct from Unknown, and the distinction is the point. Unknown is "we
	// have not established anything"; this is evidence, and it is what a logout
	// has to reach before it may report success. Folding the two together makes
	// a failed query look like a completed logout -- and makes the stale-session
	// recovery believe the line is clear when it is not.
	AuthOffline AuthState = "Offline"
)

// Connectivity is the third dimension: what can actually be reached.
//
// Separate from authentication because a gateway that answers is not the
// internet, and an HTTP 204 from a probe does not say whose session carried it.
type Connectivity string

const (
	ConnectivityUnknown Connectivity = "Unknown"
	// ConnectivityOffline -- nothing answered.
	ConnectivityOffline Connectivity = "Offline"
	// ConnectivityPortalReachable -- the gateway answers but the internet does
	// not. Spec 04 warns this is reachability, not a diagnosis of why: it does
	// not mean the password was wrong.
	ConnectivityPortalReachable Connectivity = "PortalReachable"
	// ConnectivityInternetReachable -- a probe returned its expected answer.
	ConnectivityInternetReachable Connectivity = "InternetReachable"
	// ConnectivityLimited -- something answered, but not what was expected: a
	// redirect or unexpected HTML, which is weaker evidence than a 204.
	ConnectivityLimited Connectivity = "Limited"
)

// Binding is one observation of how a line reaches the network.
//
// It is a value, not a handle: every field is what a single observation saw at
// one moment. When the address or device changes, the answer is a new Binding
// with a new Generation, never a mutated one -- so a reply that arrives from an
// in-flight request can be recognised as belonging to a line that no longer
// exists and dropped, instead of being credited to the current one.
//
// Spec 04 fixes the contents. SourceIPv4 is the address to bind a socket to;
// the address the gateway reports for the client is kept separately, because
// behind NAT they differ and binding to the remote's idea of our address fails.
type Binding struct {
	// LogicalIface is what the user selected: an OpenWrt interface name
	// ("wan"), or a Linux device name when no such interface exists ("wan.v2").
	LogicalIface string
	// L3Device is the device that actually carries layer 3 for that interface.
	// For a PPPoE or wireless uplink it is not the name of the interface.
	L3Device string
	// IfIndex distinguishes two devices that had the same name at different
	// times. A device torn down and recreated keeps its name and gets a new
	// index, and a socket bound to the old one goes nowhere.
	IfIndex int

	SourceIPv4 netip.Addr
	DNSServers []netip.Addr

	// Generation is assigned by whoever owns the transport pool, not by the
	// code that reads the interface. It is a parameter of the observation
	// rather than a field the adapter fills in, so it can never be an
	// accidental zero that compares equal to another accidental zero.
	Generation uint64
}

// Ready reports whether this binding can carry an authentication request.
func (b Binding) Ready() bool {
	return b.L3Device != "" && b.SourceIPv4.IsValid() && b.SourceIPv4.Is4()
}

// DNSIsLoopbackOnly reports that the only resolvers on this line are on this
// machine.
//
// Spec 04 refuses to treat that as proof of anything: a query to 127.0.0.1
// leaves through whichever line the local resolver chose, which may not be this
// one, so a name resolved that way is not evidence that the line works. The
// caller reports the diagnosis and asks for a resolver reachable on the line
// rather than quietly resolving through the default route.
func (b Binding) DNSIsLoopbackOnly() bool {
	if len(b.DNSServers) == 0 {
		return false
	}
	return !slices.ContainsFunc(b.DNSServers, func(server netip.Addr) bool {
		return !server.IsLoopback()
	})
}
