package daemon

import (
	"context"
	"net/netip"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// A line that could not be produced is a nil interface, not a typed nil.
//
// This is the Go trap that produces the worst kind of failure: `return client,
// err` with a nil *Client yields an interface that is not nil, so the caller's
// check for a missing line is false on exactly the path that has none, and the
// nil dereference happens several frames later inside net/http where nothing
// mentions the pool. The binding below is the zero value, which is never ready,
// so the pool refuses it.
func TestAFailedLineIsANilInterfaceRatherThanATypedNil(t *testing.T) {
	pool := transport.NewPool()
	defer pool.Close()
	lines := pooledLines{pool: pool}

	line, err := lines.Line("c1", domain.Binding{}, "http://192.0.2.1")
	if err == nil {
		t.Fatal("a line was produced for a binding that is not ready")
	}
	if line != nil {
		t.Fatal("the failure path returned a non-nil interface holding a nil pointer")
	}
}

// Retiring goes through to the pool rather than being quietly dropped.
func TestRetiringReachesThePool(t *testing.T) {
	pool := transport.NewPool()
	defer pool.Close()
	lines := pooledLines{pool: pool}

	// A complete binding: the transport refuses a line with no resolvers of its
	// own, because a line that had to borrow another one's DNS is a line whose
	// answers came from somewhere else.
	binding := domain.Binding{
		LogicalIface: "wan",
		L3Device:     "eth0",
		IfIndex:      3,
		SourceIPv4:   netip.MustParseAddr("192.0.2.10"),
		DNSServers:   []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		Generation:   1,
	}
	if _, err := lines.Line("c1", binding, "http://192.0.2.1"); err != nil {
		t.Fatalf("Line: %v", err)
	}
	if pool.Len() != 1 {
		t.Fatalf("the pool holds %d clients, want 1", pool.Len())
	}
	if retired := lines.Retire("c1", 2); retired != 1 {
		t.Errorf("retired %d clients, want the one from the older generation", retired)
	}
}

// stubRunner answers one ubus call and refuses everything else.
type stubRunner struct {
	stdout []byte
	err    error
}

func (r stubRunner) Run(context.Context, string, ...string) (openwrt.Result, error) {
	if r.err != nil {
		return openwrt.Result{}, r.err
	}
	return openwrt.Result{Stdout: r.stdout}, nil
}

func (r stubRunner) Resolve(program string) (string, error) {
	return "/usr/bin/" + program, nil
}

// The adapter's word for a link problem is what reaches the user, not this
// program's.
//
// "BindingUnavailable" is accurate and useless. An interface that is down and
// one that is waiting for DHCP need different things done about them, and only
// netifd knows which it is.
func TestTheLinkStateComesFromTheRouterRatherThanFromUs(t *testing.T) {
	binder := deviceBinder{adapter: openwrt.NewAdapter(stubRunner{
		stdout: []byte(`{"up":false,"available":true,"device":"eth0"}`),
	})}

	state, err := binder.LinkState(t.Context(), "wan")
	if err != nil {
		t.Fatalf("LinkState: %v", err)
	}
	if state != domain.LinkDown {
		t.Errorf("state = %v, want LinkDown for an interface that is not up", state)
	}
}

// And when even that cannot be read, the answer is the most cautious one rather
// than a guess that the line is fine.
func TestAnUnreadableInterfaceIsReportedAsMissing(t *testing.T) {
	binder := deviceBinder{adapter: openwrt.NewAdapter(stubRunner{
		err: domain.Errorf(domain.CodeUnsupportedCapability, "系统缺少 ubus"),
	})}

	state, err := binder.LinkState(t.Context(), "wan")
	if err == nil {
		t.Fatal("an unreadable interface was reported as readable")
	}
	if state != domain.LinkMissing {
		t.Errorf("state = %v, want LinkMissing", state)
	}
}
