package daemon

import (
	"context"
	"strconv"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/logstore"
	"github.com/matthewlu070111/smart-srun/core/internal/openwrt"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
)

type DetectEnvironmentResult struct {
	portal.Environment
	Iface string `json:"iface"`
	SSID  string `json:"ssid"`
}

func (d *Daemon) runEnvironment(ctx context.Context, request application.Request) (DetectEnvironmentResult, error) {
	result := DetectEnvironmentResult{Iface: request.Interface, SSID: request.ProbeSSID}
	fetcher, release, err := d.probeLine(ctx, request.Interface)
	if err != nil {
		return result, err
	}
	defer release()
	if guard, ok := fetcher.(probeFetcher); ok {
		guard.ssid = request.ProbeSSID
		fetcher = guard
		if err := guard.check(ctx); err != nil {
			return result, err
		}
	}
	// Candidate lookup runs in the worker, never in RPC handling/status polling.
	candidates := d.environmentCandidates(ctx, request)
	result.Environment, err = portal.DetectEnvironment(ctx, fetcher, portal.ConnectivityURLs(), candidates)
	if err == nil {
		if guard, ok := fetcher.(probeFetcher); ok {
			err = guard.check(ctx)
		}
	}
	d.log(logstore.EventDetectProbe, "", logstore.F("iface", request.Interface),
		logstore.F("checked", strconv.Itoa(len(result.CandidatesChecked))), logstore.F("acid_source", result.ACIDSource))
	return result, err
}

func deviceProbeGateways(ctx context.Context, iface string) ([]string, error) {
	// A literal Linux device has no netifd object: do not borrow another
	// interface's routes or fall back to the host's default gateway.
	if !openwrt.IsLogicalInterfaceName(iface) {
		return nil, nil
	}
	status, err := openwrt.NewAdapter(openwrt.Runner{}).InterfaceStatus(ctx, iface)
	if err != nil {
		return nil, err
	}
	var addresses []string
	for _, gateway := range status.Gateways {
		addresses = append(addresses, "http://"+gateway.String())
	}
	return addresses, nil
}

func (d *Daemon) environmentCandidates(ctx context.Context, r application.Request) []portal.Candidate {
	var candidates []portal.Candidate
	seen := map[string]bool{}
	add := func(raw, source string) {
		address := portal.Address(raw)
		if address != "" && !seen[address] && len(candidates) < portal.MaxCandidates {
			seen[address] = true
			candidates = append(candidates, portal.Candidate{URL: address, Source: source})
		}
	}
	add(r.ProbeURL, "填写的认证地址")
	if r.ProbeSchool != "" {
		// Prefer the explicit local preset, then the installed/cached public
		// catalogue. Neither read refreshes the catalogue over the network.
		found := false
		if users, err := d.users.Get(); err == nil {
			for _, p := range users.Presets {
				if p.School.ShortName == r.ProbeSchool {
					add(p.School.Defaults.BaseURL, "学校预设")
					found = true
					break
				}
			}
		}
		if !found {
			if schools, err := d.readPublicPresets(); err == nil {
				for _, p := range schools {
					if p.ShortName == r.ProbeSchool {
						add(p.Defaults.BaseURL, "学校预设")
						break
					}
				}
			}
		}
	}
	cfg := d.config.Snapshot()
	for _, account := range cfg.CampusAccounts {
		if account.AccessMode != domain.AccessMode(r.ProbeMode) {
			continue
		}
		if account.IsWired() {
			if account.WiredIface != r.Interface {
				continue
			}
		} else if cfg.STAIface != r.Interface || r.ProbeSSID == "" || account.SSID != r.ProbeSSID {
			continue
		}
		add(account.BaseURL, "该线路已有账号")
	}
	if len(candidates) < portal.MaxCandidates && d.probeGateways != nil {
		if gateways, err := d.probeGateways(ctx, r.Interface); err == nil {
			for _, gateway := range gateways {
				add(gateway, "所选出口的网关")
			}
		}
	}
	return candidates
}
