// Package integration holds the tests that need a real network to mean
// anything.
//
// Everything in the transport package's own tests runs on loopback, which can
// show that a socket was configured but not where its packets went. Spec 10 is
// explicit that the exit has to be observed rather than asserted, and these
// tests are that observation: two lines whose gateways answer on the SAME
// address with different content, so the only thing that can decide which one a
// request reaches is the device binding.
//
// They need a topology that only root with iproute2 and veth can build, which
// on this project means the OpenWrt guest.
// .codex/go-loop/tools/netns_gate.py builds it, runs this, and tears it down;
// evidence/m05-environment.json records why the development host cannot.
package integration

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

// line describes one of the two lines the topology provides.
type line struct {
	device string
	source netip.Addr
	answer string
}

// topology reads what netns_gate.py built. The test says what it needs and the
// setup script says what it made, rather than both hard-coding the same
// addresses in two places that can drift apart.
func topology(t *testing.T) (gateway string, one, two line) {
	t.Helper()
	if os.Getenv("SMART_SRUN_NETNS") != "1" {
		t.Skip("no namespace topology; run .codex/go-loop/tools/netns_gate.py")
	}

	must := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s is not set; the topology is incomplete", name)
		}
		return value
	}
	address := func(name string) netip.Addr {
		parsed, err := netip.ParseAddr(must(name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return parsed
	}

	return must("SMART_SRUN_GATEWAY"),
		line{must("SMART_SRUN_LINE1_DEVICE"), address("SMART_SRUN_LINE1_SOURCE"),
			must("SMART_SRUN_LINE1_ANSWER")},
		line{must("SMART_SRUN_LINE2_DEVICE"), address("SMART_SRUN_LINE2_SOURCE"),
			must("SMART_SRUN_LINE2_ANSWER")}
}

func bindingFor(l line, generation uint64) domain.Binding {
	return domain.Binding{
		LogicalIface: l.device,
		L3Device:     l.device,
		SourceIPv4:   l.source,
		// The gateway address is reachable without a lookup, so the resolver is
		// never consulted; a server is still required for the client to be
		// built at all.
		DNSServers: []netip.Addr{netip.MustParseAddr("192.0.2.53")},
		Generation: generation,
	}
}

func fetch(t *testing.T, client *transport.Client, url string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer transport.DrainAndClose(response.Body)

	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// T17 -- each line reaches its own gateway, and the two gateways share an
// address.
//
// The host has a route to that address through each device at different
// metrics. A client that set only the source would follow the metric and reach
// the same namespace both times, so the second line's answer is the whole
// evidence: it can only have come from the device binding.
func TestEachLineReachesItsOwnGateway(t *testing.T) {
	gateway, one, two := topology(t)
	url := "http://" + gateway + "/answer"

	for _, l := range []line{one, two} {
		t.Run(l.device, func(t *testing.T) {
			client, err := transport.NewClient(bindingFor(l, 1))
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			defer client.Close()

			body, err := fetch(t, client, url)
			if err != nil {
				t.Fatalf("fetch over %s: %v", l.device, err)
			}
			if body != l.answer {
				t.Errorf("%s reached the gateway answering %q, want %q -- the "+
					"request left by the wrong line", l.device, body, l.answer)
			}
		})
	}
}

// T17 -- and they do so at the same time without crossing.
//
// One shared route table, one shared kernel, two workers. If the binding were a
// process-wide setting rather than a per-socket one, this is where it would
// show.
func TestConcurrentLinesDoNotCrossOver(t *testing.T) {
	gateway, one, two := topology(t)
	url := "http://" + gateway + "/answer"

	type result struct {
		device string
		body   string
		err    error
	}
	results := make(chan result, 40)

	var wait sync.WaitGroup
	for _, l := range []line{one, two} {
		for range 10 {
			wait.Add(1)
			go func(l line) {
				defer wait.Done()
				client, err := transport.NewClient(bindingFor(l, 1))
				if err != nil {
					results <- result{l.device, "", err}
					return
				}
				defer client.Close()
				body, err := fetch(t, client, url)
				results <- result{l.device, body, err}
			}(l)
		}
	}
	wait.Wait()
	close(results)

	expected := map[string]string{one.device: one.answer, two.device: two.answer}
	seen := 0
	for item := range results {
		seen++
		if item.err != nil {
			t.Errorf("%s: %v", item.device, item.err)
			continue
		}
		if item.body != expected[item.device] {
			t.Errorf("%s reached the gateway answering %q, want %q -- two "+
				"concurrent lines crossed", item.device, item.body,
				expected[item.device])
		}
	}
	if seen != 20 {
		t.Errorf("collected %d results, want 20", seen)
	}
}

// T14 -- a line whose device is down fails, even though the other line works
// and the default route is fine.
//
// This is the failure a client without a device binding gets wrong: the route
// table still has a way to the gateway, so an unbound request succeeds and the
// user is told they are online on a line that is not carrying anything.
func TestALineWhoseDeviceIsDownFailsWhileTheOtherWorks(t *testing.T) {
	gateway, one, two := topology(t)
	url := "http://" + gateway + "/answer"

	// Take the second line's device down and put it back afterwards, whatever
	// happens in between.
	if err := setLink(two.device, false); err != nil {
		t.Skipf("cannot bring %s down: %v", two.device, err)
	}
	t.Cleanup(func() { setLink(two.device, true) })
	time.Sleep(300 * time.Millisecond)

	down, err := transport.NewClient(bindingFor(two, 2))
	if err == nil {
		defer down.Close()
		body, fetchErr := fetch(t, down, url)
		if fetchErr == nil {
			t.Errorf("a request over the down line succeeded and returned %q; "+
				"it left by the line that was still up", body)
		}
	}

	// The other line is unaffected, so the failure above is the device being
	// down and not the topology having collapsed.
	up, err := transport.NewClient(bindingFor(one, 2))
	if err != nil {
		t.Fatalf("NewClient for the working line: %v", err)
	}
	defer up.Close()

	body, err := fetch(t, up, url)
	if err != nil {
		t.Fatalf("the working line failed too: %v", err)
	}
	if body != one.answer {
		t.Errorf("the working line answered %q, want %q", body, one.answer)
	}
}
