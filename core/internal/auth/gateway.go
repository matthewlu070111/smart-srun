// Package auth performs one authentication transaction against an SRun
// gateway: fetch a challenge, build the login, send it, then verify what
// actually happened.
//
// It owns the sequence and nothing else. The bytes on the wire come from
// protocol/srun, which is pure and already checked against a frozen oracle; the
// connection comes from transport, which is bound to one line. What is decided
// here is the order of the steps, what each answer means, and -- the part spec
// 04 spends the most words on -- what this program is allowed to do about a
// session that belongs to somebody else.
package auth

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// The paths an SRun portal serves. Fixed rather than configurable: they are the
// protocol, and a school that moved them would need a strategy, not a setting.
const (
	challengePath = "/cgi-bin/get_challenge"
	portalPath    = "/cgi-bin/srun_portal"
	onlinePath    = "/cgi-bin/rad_user_info"
	// logoutPath is where a signed logout goes, and it is not the portal.
	//
	// Spec 04 names it, and the baseline posts there: srun_auth.logout() takes
	// rad_user_dm_api, built from SchoolProfile.API_RAD_USER_DM. Sending the
	// signed form to srun_portal instead means a gateway that implements this
	// endpoint never receives the unbind, so the session stays up while this
	// program reports it gone -- and the stale-session recovery that depends on
	// the unbind silently does nothing.
	logoutPath = "/cgi-bin/rad_user_dm"
)

// Gateway is where one account authenticates.
type Gateway struct {
	// BaseURL is the origin the user configured, such as
	// "http://10.0.0.1". Any path on it is discarded: the endpoints above are
	// the protocol's, and appending them to a user-supplied path would produce
	// a URL nobody meant.
	BaseURL string
	ACID    string
}

// ParseGateway checks a configured base URL and reduces it to an origin.
func ParseGateway(baseURL, acID string) (Gateway, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return Gateway{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"base_url", "未配置认证地址")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Gateway{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"base_url", "认证地址无法解析").Wrap(err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Gateway{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"base_url", "认证地址必须以 http:// 或 https:// 开头")
	}
	if parsed.Host == "" {
		return Gateway{}, domain.FieldErrorf(domain.CodeInvalidConfig,
			"base_url", "认证地址缺少主机名")
	}

	return Gateway{
		BaseURL: parsed.Scheme + "://" + parsed.Host,
		ACID:    strings.TrimSpace(acID),
	}, nil
}

// challengeURL asks for the token a login has to be signed with.
//
// The timestamp is in milliseconds, and the callback is a name the parser will
// accept. Both are arguments here rather than read from a clock inside the
// protocol layer, which is what keeps that layer testable against fixed
// vectors.
func (g Gateway) challengeURL(username string, milliseconds int64, callback string) string {
	query := url.Values{}
	query.Set("callback", callback)
	query.Set("username", username)
	query.Set("_", strconv.FormatInt(milliseconds, 10))
	return g.BaseURL + challengePath + "?" + query.Encode()
}

// loginURL carries the whole credentialed request.
//
// Every field spec 04 lists is present and in the list it fixes. The password
// on the wire is the HMAC with its {MD5} marker, never the password itself;
// the checksum is over the bare digest, which is a distinction the baseline got
// right and is easy to get wrong.
func (g Gateway) loginURL(p loginParams) string {
	query := url.Values{}
	query.Set("callback", p.callback)
	query.Set("action", "login")
	query.Set("username", p.username)
	query.Set("password", p.wirePassword)
	query.Set("ac_id", g.ACID)
	query.Set("ip", p.ip)
	query.Set("chksum", p.checksum)
	query.Set("info", p.info)
	query.Set("n", p.n)
	query.Set("type", p.loginType)
	query.Set("os", p.os)
	query.Set("name", p.name)
	query.Set("double_stack", p.doubleStack)
	query.Set("_", strconv.FormatInt(p.milliseconds, 10))
	return g.BaseURL + portalPath + "?" + query.Encode()
}

// logoutURL ends a session.
//
// The times here are seconds, not milliseconds. Spec 04 calls that out because
// the two are different parameters with different units in the same protocol,
// and signing with the wrong one produces a signature the gateway rejects
// without saying why.
// The parameters are the baseline's build_logout_params and no more: callback,
// time, unbind, ip, username, sign. There is no action and no ac_id here --
// those belong to the portal form of the request, and carrying them along would
// be inventing a third shape that neither the baseline nor the spec describes.
func (g Gateway) logoutURL(username, ip, sign string, seconds int64, callback string) string {
	query := url.Values{}
	query.Set("callback", callback)
	query.Set("time", strconv.FormatInt(seconds, 10))
	query.Set("unbind", srunLogoutUnbind)
	query.Set("ip", ip)
	query.Set("username", username)
	query.Set("sign", sign)
	return g.BaseURL + logoutPath + "?" + query.Encode()
}

// onlineURL asks who, if anyone, is authenticated on this line.
func (g Gateway) onlineURL(milliseconds int64, callback string) string {
	query := url.Values{}
	query.Set("callback", callback)
	query.Set("_", strconv.FormatInt(milliseconds, 10))
	return g.BaseURL + onlinePath + "?" + query.Encode()
}

// loginParams is everything the login URL needs, assembled by the transaction
// so the URL builder has no logic of its own.
type loginParams struct {
	callback     string
	username     string
	wirePassword string
	ip           string
	checksum     string
	info         string
	n            string
	loginType    string
	os           string
	name         string
	doubleStack  string
	milliseconds int64
}
