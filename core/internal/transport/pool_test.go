package transport

import (
	"net/netip"
	"sync"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// poolFor builds a pool whose clients need no real DNS servers, so the keying
// and retirement can be tested without a network.
func poolFor(closed *[]uint64) *Pool {
	var mu sync.Mutex
	pool := NewPool()
	pool.build = func(binding domain.Binding) (*Client, error) {
		dialer := NewDialer(binding)
		var devices []string
		dialer.control = noControl(&devices)
		resolver := &Resolver{
			dialer:  dialer,
			servers: []netip.Addr{netip.MustParseAddr("192.0.2.53")},
			port:    DNSPort,
		}
		client := newClientWith(binding, dialer, resolver)
		// Record retirements by generation, so a test can see which clients
		// were closed rather than only which keys are gone from the map.
		generation := binding.Generation
		if closed != nil {
			client.onClose = func() {
				mu.Lock()
				*closed = append(*closed, generation)
				mu.Unlock()
			}
		}
		return client, nil
	}
	return pool
}

func bindingAt(generation uint64, device, source string) domain.Binding {
	return domain.Binding{
		LogicalIface: "wan",
		L3Device:     device,
		IfIndex:      1,
		SourceIPv4:   netip.MustParseAddr(source),
		DNSServers:   []netip.Addr{netip.MustParseAddr("192.0.2.53")},
		Generation:   generation,
	}
}

// The same account on the same binding gets the same client, or every request
// would pay for a new connection.
func TestTheSameBindingReusesOneClient(t *testing.T) {
	pool := poolFor(nil)
	binding := bindingAt(1, "eth1", "192.0.2.10")

	first, err := pool.Get("c1", binding, "10.0.0.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, err := pool.Get("c1", binding, "10.0.0.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if first != second {
		t.Error("the same binding produced two clients")
	}
	if pool.Len() != 1 {
		t.Errorf("pool holds %d clients, want 1", pool.Len())
	}
}

// T15 -- a new generation replaces the old one and closes it.
//
// This is the DHCP case. A connection opened before the address changed carries
// the old source, and reusing it sends the gateway a request from an address
// the line no longer has.
func TestANewGenerationRetiresTheOldClient(t *testing.T) {
	var closed []uint64
	pool := poolFor(&closed)

	if _, err := pool.Get("c1", bindingAt(1, "eth1", "192.0.2.10"),
		"10.0.0.1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The lease changed: same device, new address, new generation.
	if _, err := pool.Get("c1", bindingAt(2, "eth1", "192.0.2.11"),
		"10.0.0.1"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if pool.Len() != 1 {
		t.Errorf("pool holds %d clients, want only the current one: %+v",
			pool.Len(), pool.Keys())
	}
	if len(closed) != 1 || closed[0] != 1 {
		t.Errorf("closed generations = %v, want the old one", closed)
	}
	if keys := pool.Keys(); len(keys) == 1 && keys[0].Generation != 2 {
		t.Errorf("the surviving client is generation %d", keys[0].Generation)
	}
}

// Different accounts on one line do not share a client, so a gateway that
// tracks a session per connection does not see one user where there are two.
func TestTwoAccountsOnOneLineDoNotShareAClient(t *testing.T) {
	pool := poolFor(nil)
	binding := bindingAt(1, "eth1", "192.0.2.10")

	first, _ := pool.Get("c1", binding, "10.0.0.1")
	second, _ := pool.Get("c2", binding, "10.0.0.1")

	if first == second {
		t.Error("two accounts were given the same client")
	}
	if pool.Len() != 2 {
		t.Errorf("pool holds %d clients, want 2", pool.Len())
	}
}

// And one account's retirement does not disturb another's.
func TestRetiringOneAccountLeavesTheOthers(t *testing.T) {
	var closed []uint64
	pool := poolFor(&closed)
	binding := bindingAt(1, "eth1", "192.0.2.10")

	pool.Get("c1", binding, "10.0.0.1")
	pool.Get("c2", binding, "10.0.0.1")

	if n := pool.RetireAccount("c1"); n != 1 {
		t.Errorf("retired %d clients, want 1", n)
	}
	keys := pool.Keys()
	if len(keys) != 1 || keys[0].AccountID != "c2" {
		t.Errorf("remaining = %+v, want only c2", keys)
	}
}

// Two lines that reach the same gateway address must not share a pool: that is
// the overlapping-subnet case, and sharing would send one line's request out of
// the other.
func TestTwoLinesToTheSameGatewayAddressAreSeparate(t *testing.T) {
	pool := poolFor(nil)

	first, _ := pool.Get("c1", bindingAt(1, "eth1", "192.0.2.10"), "10.0.0.1")
	second, _ := pool.Get("c1", bindingAt(1, "eth2", "198.51.100.10"), "10.0.0.1")

	if first == second {
		t.Fatal("two different lines to the same gateway address shared a client")
	}
	if pool.Len() != 2 {
		t.Errorf("pool holds %d clients, want 2", pool.Len())
	}
}

// A generation of zero would compare equal to every other unset one, so two
// separate observations would share a pool. The caller owns generations.
func TestAZeroGenerationIsRefused(t *testing.T) {
	pool := poolFor(nil)
	_, err := pool.Get("c1", bindingAt(0, "eth1", "192.0.2.10"), "10.0.0.1")
	if err == nil {
		t.Fatal("a binding with no generation was accepted")
	}
	if pool.Len() != 0 {
		t.Errorf("pool holds %d clients after a refusal", pool.Len())
	}
}

// A key without an account would merge accounts.
func TestAnEmptyAccountIsRefused(t *testing.T) {
	pool := poolFor(nil)
	if _, err := pool.Get("", bindingAt(1, "eth1", "192.0.2.10"),
		"10.0.0.1"); err == nil {
		t.Fatal("a client with no account was created")
	}
}

// T15 -- the check spec 04 requires immediately before credentials go out.
//
// A DHCP change between fetching the challenge and posting the login makes the
// whole attempt invalid; sending anyway authenticates with an address the
// gateway will not recognise.
func TestAClientFromAnOldGenerationIsNotCurrent(t *testing.T) {
	pool := poolFor(nil)
	client, err := pool.Get("c1", bindingAt(3, "eth1", "192.0.2.10"), "10.0.0.1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !pool.StillCurrent(client, 3) {
		t.Error("the current generation was reported as stale")
	}
	if pool.StillCurrent(client, 4) {
		t.Error("a client from the previous generation was reported as current; " +
			"credentials would go out on a binding that no longer exists")
	}
	if pool.StillCurrent(nil, 3) {
		t.Error("a missing client was reported as current")
	}
}

// Closing the pool releases everything, so a shutdown does not leave sockets.
func TestClosingThePoolReleasesEverything(t *testing.T) {
	var closed []uint64
	pool := poolFor(&closed)

	pool.Get("c1", bindingAt(1, "eth1", "192.0.2.10"), "10.0.0.1")
	pool.Get("c2", bindingAt(1, "eth1", "192.0.2.10"), "10.0.0.1")
	pool.Close()

	if pool.Len() != 0 {
		t.Errorf("pool holds %d clients after Close", pool.Len())
	}
	if len(closed) != 2 {
		t.Errorf("closed %d clients, want 2", len(closed))
	}
}

// The pool is reached from more than one worker, so it has to be safe to use
// from several at once. Run under -race this is the test that would find it.
func TestThePoolIsSafeUnderConcurrentUse(t *testing.T) {
	pool := poolFor(nil)

	var wait sync.WaitGroup
	for worker := range 8 {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			account := string(rune('a' + worker%3))
			for generation := uint64(1); generation <= 20; generation++ {
				pool.Get(account, bindingAt(generation, "eth1", "192.0.2.10"),
					"10.0.0.1")
				pool.Keys()
				pool.Len()
			}
		}(worker)
	}
	wait.Wait()

	// One client per account: every older generation was either retired when a
	// newer one arrived, or refused when it arrived late.
	if pool.Len() > 3 {
		t.Errorf("pool holds %d clients for 3 accounts; a stale generation "+
			"survived: %+v", pool.Len(), pool.Keys())
	}
	for _, key := range pool.Keys() {
		if key.Generation != 20 {
			t.Errorf("generation %d for %s outlived the newest",
				key.Generation, key.AccountID)
		}
	}
}

// T15 -- a worker holding an observation from before the line changed is told
// to re-resolve, not handed a client for it.
//
// Without this the stale binding goes back into the pool and sits there, and
// credentials can leave on an address the line has already given up. The
// concurrency test above is what surfaced it.
func TestAStaleGenerationIsRefusedRatherThanRevived(t *testing.T) {
	pool := poolFor(nil)

	if _, err := pool.Get("c1", bindingAt(5, "eth1", "192.0.2.11"),
		"10.0.0.1"); err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, err := pool.Get("c1", bindingAt(4, "eth1", "192.0.2.10"), "10.0.0.1")
	if err == nil {
		t.Fatal("a client was built for a generation the line has moved past")
	}
	if code := codeOf(t, err); code != domain.CodeBindingChanged {
		t.Errorf("code = %s, want BindingChanged", code)
	}
	if pool.Len() != 1 {
		t.Errorf("pool holds %d clients, want only the current one", pool.Len())
	}
}

// An account that was removed and comes back starts fresh, or a re-added
// account with a counter that restarts would be permanently unservable.
func TestARemovedAccountCanComeBackAtAnyGeneration(t *testing.T) {
	pool := poolFor(nil)

	pool.Get("c1", bindingAt(9, "eth1", "192.0.2.10"), "10.0.0.1")
	pool.RetireAccount("c1")

	if _, err := pool.Get("c1", bindingAt(1, "eth1", "192.0.2.10"),
		"10.0.0.1"); err != nil {
		t.Errorf("a re-added account was refused: %v", err)
	}
}
