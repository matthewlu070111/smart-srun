package transport

import (
	"net/netip"
	"slices"
	"sync"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ClientKey is what makes two clients the same client.
//
// Spec 04 sets the parts: the account, the binding's generation, the device,
// the source address and the gateway. Leaving any of them out merges two things
// that must not share a connection:
//
//   - without the account, two accounts on one line reuse each other's
//     connections, and a gateway that tracks sessions per connection sees one
//     user where there are two;
//   - without the generation, a connection opened before a DHCP change is
//     reused after it, carrying a source address the line no longer has;
//   - without the device or the source, two lines that happen to reach the same
//     gateway address share a pool, which is exactly the confusion this package
//     exists to prevent.
type ClientKey struct {
	AccountID  string
	Generation uint64
	Device     string
	Source     netip.Addr
	Gateway    string
}

// Pool hands out one client per key and closes the ones that are no longer
// current.
//
// It is not a cache in the usual sense: nothing here is kept because it might
// be useful later. A client exists while its binding is the current one for its
// account, and stops existing the moment a newer generation appears. Spec 04
// requires the old idle connections to be closed at that point rather than left
// to time out, because a connection is only harmless while the address it was
// opened with still belongs to the line.
type Pool struct {
	mu      sync.Mutex
	clients map[ClientKey]*Client
	// newest is the highest generation seen per account, so a worker arriving
	// late with an older observation can be told to re-resolve rather than
	// given a client for a line that has already moved on.
	newest map[string]uint64
	// build is the constructor, so a test can supply a client without a
	// resolver that would need real DNS servers.
	build func(domain.Binding) (*Client, error)
}

// NewPool builds an empty pool.
func NewPool() *Pool {
	return &Pool{
		clients: map[ClientKey]*Client{},
		newest:  map[string]uint64{},
		build:   NewClient,
	}
}

// Get returns the client for one account on one binding, making it if needed.
//
// Asking for a newer generation retires the older ones for that account: this
// is the single point where a binding change turns into closed connections, so
// there is no path that creates the new client and forgets the old.
func (p *Pool) Get(accountID string, binding domain.Binding, gateway string) (*Client, error) {
	if accountID == "" {
		return nil, domain.Errorf(domain.CodeInternal,
			"连接池的键必须包含账号")
	}
	if binding.Generation == 0 {
		// A zero generation would compare equal to every other unset one, so
		// two different observations would share a pool. The caller owns
		// generations and must supply a real one.
		return nil, domain.Errorf(domain.CodeInternal,
			"绑定代次为 0；调用方必须提供本次观测的代次")
	}
	if !binding.Ready() {
		return nil, domain.FieldErrorf(domain.CodeBindingUnavailable,
			"wired_iface", "线路 %s 尚未就绪", binding.LogicalIface)
	}

	key := ClientKey{
		AccountID:  accountID,
		Generation: binding.Generation,
		Device:     binding.L3Device,
		Source:     binding.SourceIPv4,
		Gateway:    gateway,
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[key]; ok {
		return client, nil
	}

	// A worker that observed the line before it changed can still arrive here
	// afterwards, holding the older generation. Building a client for it would
	// put a stale binding back into the pool, where it would sit until
	// something else retired it -- and in the meantime credentials could go out
	// on an address the line has given up. The concurrency test found this by
	// interleaving generations from several goroutines.
	if seen, ok := p.newest[accountID]; ok && binding.Generation < seen {
		return nil, domain.Errorf(domain.CodeBindingChanged,
			"线路已经变化：当前代次 %d，本次请求用的是 %d；请重新解析绑定后再试",
			seen, binding.Generation)
	}

	client, err := p.build(binding)
	if err != nil {
		return nil, err
	}

	p.retireOlderLocked(accountID, binding.Generation)
	p.clients[key] = client
	p.newest[accountID] = binding.Generation
	return client, nil
}

// Retire closes every client for an account older than a generation.
//
// Called when a binding changes without a new request following it -- an
// interface going down, an account being disabled -- so the connections do not
// sit open until their idle timeout.
func (p *Pool) Retire(accountID string, currentGeneration uint64) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.retireOlderLocked(accountID, currentGeneration)
}

func (p *Pool) retireOlderLocked(accountID string, currentGeneration uint64) int {
	closed := 0
	for key, client := range p.clients {
		if key.AccountID != accountID || key.Generation >= currentGeneration {
			continue
		}
		client.Close()
		delete(p.clients, key)
		closed++
	}
	return closed
}

// RetireAccount closes everything belonging to one account, whatever its
// generation. For an account being removed or disabled.
func (p *Pool) RetireAccount(accountID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	closed := 0
	for key, client := range p.clients {
		if key.AccountID != accountID {
			continue
		}
		client.Close()
		delete(p.clients, key)
		closed++
	}
	// The account is gone, so the generation it reached is no longer a reason
	// to refuse anything. Keeping it would make a re-added account with a fresh
	// counter permanently unservable.
	delete(p.newest, accountID)
	return closed
}

// Close releases everything. For shutdown.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, client := range p.clients {
		client.Close()
		delete(p.clients, key)
	}
	clear(p.newest)
}

// Len reports how many clients are held, which is what a leak looks like when
// it grows without bound.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients)
}

// Keys lists what the pool holds, sorted, for a diagnosis and for the tests
// that check a retirement really removed something.
func (p *Pool) Keys() []ClientKey {
	p.mu.Lock()
	defer p.mu.Unlock()

	keys := make([]ClientKey, 0, len(p.clients))
	for key := range p.clients {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b ClientKey) int {
		if a.AccountID != b.AccountID {
			return compareString(a.AccountID, b.AccountID)
		}
		if a.Generation != b.Generation {
			return int(a.Generation) - int(b.Generation)
		}
		return compareString(a.Gateway, b.Gateway)
	})
	return keys
}

func compareString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// StillCurrent reports whether a client may still be used to send credentials.
//
// Spec 04 requires this check immediately before the credentials go out: a DHCP
// change between fetching the challenge and posting the login makes the whole
// attempt invalid, and sending anyway means authenticating with an address the
// gateway will not recognise -- or worse, authenticating the wrong line.
func (p *Pool) StillCurrent(client *Client, currentGeneration uint64) bool {
	if client == nil {
		return false
	}
	return client.Binding().Generation == currentGeneration
}
