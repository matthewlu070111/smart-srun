package portal

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// EnvironmentBudget includes connectivity checks and all candidate pages.
// Shorter per-source deadlines leave room for a later source to answer.
const EnvironmentBudget = 20 * time.Second
const MaxCandidates = 4

// ConnectivityURLs preserves the baseline's ordered, credential-free probes.
// Config v2 currently has no user-editable endpoint list.
func ConnectivityURLs() []string {
	return []string{
		"http://connect.rom.miui.com/generate_204",
		"http://connectivitycheck.platform.hicloud.com/generate_204",
		"http://wifi.vivo.com.cn/generate_204",
	}
}

type Candidate struct{ URL, Source string }

type Environment struct {
	OK                bool     `json:"ok"`
	State             string   `json:"state"`
	StatusCode        int      `json:"status_code"`
	CheckedURL        string   `json:"checked_url"`
	PortalURL         string   `json:"portal_url"`
	BaseURL           string   `json:"base_url"`
	ACID              string   `json:"acid"`
	ACIDSource        string   `json:"acid_source"`
	AddressSource     string   `json:"address_source"`
	CandidatesChecked []string `json:"candidates_checked"`
	Message           string   `json:"message"`
}

// DetectEnvironment distinguishes Internet evidence from portal evidence. A
// successful 204 never becomes an authentication address. A failed probe says
// nothing about an account's password or the owner of an existing session.
func DetectEnvironment(ctx context.Context, client Fetcher, endpoints []string, candidates []Candidate) (Environment, error) {
	ctx, cancel := context.WithTimeout(ctx, EnvironmentBudget)
	defer cancel()
	result := Environment{State: "down", CandidatesChecked: []string{}, Message: "无法访问连通性检测服务器，请检查所选线路"}
	connectivityHost := func(address string) bool {
		parsed, err := url.Parse(address)
		if err != nil {
			return true
		}
		for _, endpoint := range endpoints {
			check, err := url.Parse(endpoint)
			if err == nil && parsed.Hostname() == check.Hostname() {
				return true
			}
		}
		return false
	}
	for index, endpoint := range endpoints {
		if index == 3 {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		endpoint = Address(endpoint)
		if endpoint == "" {
			continue
		}
		probeCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		status, body, location, err := fetch(probeCtx, client, endpoint)
		stop()
		if fatalDiscoveryError(err) {
			return result, err
		}
		if err != nil {
			continue
		}
		result.CheckedURL, result.StatusCode = endpoint, status
		if status == http.StatusNoContent {
			result.State = "online"
			result.Message = "未识别到认证地址。可在下一步选择学校预设或填写登录页地址。"
			break
		}
		if location == "" {
			location = RedirectFromHTML(body)
		}
		lower := bytes.ToLower(body)
		if location == "" && !bytes.Contains(lower, []byte("<html")) && !bytes.Contains(lower, []byte("<form")) && !bytes.Contains(lower, []byte("<input")) {
			continue
		}
		result.State, result.Message = "portal", "出口被拦截，但响应里没有可用的认证地址"
		// Intercepted HTML without a destination is evidence of restriction,
		// not evidence that the connectivity server is the campus gateway.
		target := ""
		if location != "" {
			target = Address(Join(endpoint, location))
		}
		if target != "" {
			if !connectivityHost(target) {
				result.OK, result.PortalURL, result.BaseURL = true, target, Origin(target)
				result.AddressSource, result.Message = "门户跳转", "已捕获认证地址，但未发现 AC_ID"
			}
			probeCtx, stop = context.WithTimeout(ctx, 3*time.Second)
			finding, probeErr := Probe(probeCtx, client, target)
			stop()
			if fatalDiscoveryError(probeErr) {
				return result, probeErr
			}
			if probeErr == nil && !connectivityHost(finding.URL) {
				if finding.ACID != "" {
					result.accept(finding, "门户跳转")
				} else {
					result.OK, result.PortalURL, result.BaseURL = true, finding.URL, Origin(finding.URL)
					result.AddressSource, result.Message = "门户跳转", "已捕获认证地址，但未发现 AC_ID"
				}
			}
		}
		break
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.OK {
		return result, nil
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if len(seen) == MaxCandidates {
			break
		}
		address := Address(candidate.URL)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.CandidatesChecked = append(result.CandidatesChecked, candidate.Source+"："+address)
		probeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		finding, err := Probe(probeCtx, client, address)
		stop()
		if fatalDiscoveryError(err) {
			return result, err
		}
		if err == nil && finding.ACID != "" {
			result.accept(finding, candidate.Source)
			break
		}
	}
	return result, ctx.Err()
}

func (e *Environment) accept(f Finding, source string) {
	e.OK, e.BaseURL, e.PortalURL = true, Origin(f.URL), f.URL
	e.ACID, e.ACIDSource, e.AddressSource = f.ACID, f.Source, source
	e.Message = "认证参数来源：" + source + "。"
}

// Ordinary transport failure may try another candidate. Losing the selected
// line must stop the whole task, even if an earlier response looked useful.
func fatalDiscoveryError(err error) bool {
	code, _ := domain.CodeOf(err)
	return code == domain.CodeBindingChanged || code == domain.CodeBindingUnavailable || code == domain.CodeNotFound
}
