package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/matthewlu070111/smart-srun/core/internal/application"
	"github.com/matthewlu070111/smart-srun/core/internal/auth"
	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

const verifyBudget = 180 * time.Second

type VerificationParams struct {
	DetectACIDParams
	UserID      string            `json:"user_id"`
	Password    string            `json:"password,omitempty"`
	Candidates  []string          `json:"candidates,omitempty"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
	Login       domain.LoginShape `json:"login,omitempty"`
}

type VerificationAttempt struct {
	Suffix   string `json:"suffix"`
	Username string `json:"username"`
	Outcome  string `json:"outcome"`
	Message  string `json:"message"`
}
type VerificationResult struct {
	OK               bool                  `json:"ok"`
	Confirmed        bool                  `json:"confirmed"`
	Suffix           string                `json:"suffix"`
	Attempts         []VerificationAttempt `json:"attempts"`
	Source           string                `json:"source"`
	PasswordVerified bool                  `json:"password_verified"`
	Message          string                `json:"message"`
}

func (d *Daemon) detectIdentity(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.submitVerification(ctx, raw, application.KindDetectIdentity)
}
func (d *Daemon) detectVerify(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.submitVerification(ctx, raw, application.KindDetectVerify)
}
func (d *Daemon) submitVerification(ctx context.Context, raw json.RawMessage, kind application.Kind) (any, error) {
	var p VerificationParams
	if err := control.DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	if kind == application.KindDetectIdentity && (p.Password != "" || len(p.Candidates) != 0 || p.MaxAttempts != 0) {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "只读身份查询不接受密码或登录候选")
	}
	if len(p.Candidates) > 5 || p.MaxAttempts < 0 || p.MaxAttempts > 5 {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "最多验证五个已知后缀")
	}
	if kind == application.KindDetectVerify && (p.Password == "" || len(p.Candidates) == 0) {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "验证需要密码和显式选择的后缀")
	}
	if p.MaxAttempts > 0 && p.MaxAttempts < len(p.Candidates) {
		p.Candidates = p.Candidates[:p.MaxAttempts]
	}
	// Validate auth fields using the same contract as a saved account, without
	// saving it or borrowing the active account's resolved login parameters.
	cfg := config.Defaults()
	account := domain.CampusAccount{ID: "wizard", UserID: p.UserID, Password: p.Password, AccessMode: domain.AccessModeWired, WiredIface: p.Iface, Login: p.Login}
	cfg.CampusAccounts = []domain.CampusAccount{account}
	cfg = config.Normalize(cfg)
	if err := config.Validate(cfg); err != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "账号或高级认证参数无效")
	}
	for _, suffix := range p.Candidates {
		if !validVerifiedSuffix(suffix) {
			return nil, domain.Errorf(domain.CodeInvalidArgument, "认证后缀无效，空字符串表示不加后缀")
		}
	}
	p.Login = cfg.CampusAccounts[0].Login
	private, err := json.Marshal(p)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidArgument, "认证参数无法编码")
	}
	return d.submitProbe(ctx, p.DetectACIDParams, kind, string(private))
}

func validVerifiedSuffix(s string) bool {
	return len(s) <= config.MaxSuffixBytes && s != "??" && !strings.ContainsAny(s, "@, \t\r\n\x00")
}

func identityFinding(identity auth.Identity, user string) VerificationResult {
	r := VerificationResult{Attempts: []VerificationAttempt{}, Source: "online_identity", Message: "未找到可确认的在线账号，请选择后缀后验证登录"}
	if !identity.Present {
		return r
	}
	reported := identity.Username
	if reported == user && strings.Contains(user, "@") {
		r.OK, r.Confirmed = true, true
	} else if suffix := strings.TrimPrefix(reported, user+"@"); strings.HasPrefix(reported, user+"@") && suffix != "" && validVerifiedSuffix(suffix) {
		r.OK, r.Confirmed, r.Suffix = true, true, strings.TrimPrefix(reported, user+"@")
	} else if reported == user {
		r.Message = "在线账号未包含认证后缀，请手动选择。"
		return r
	} else {
		r.Message = "在线账号与填写的校园网账号不匹配，请核对账号。"
		return r
	}
	r.Message = "已从网关当前在线账号确认登录后缀；本次填写的密码尚未验证"
	return r
}

func (d *Daemon) runVerification(ctx context.Context, request application.Request, report func(application.Phase)) (result VerificationResult, err error) {
	var p VerificationParams
	if err := json.Unmarshal([]byte(request.PrivateJSON), &p); err != nil {
		return result, domain.Errorf(domain.CodeInternal, "认证任务参数不可用")
	}
	fetcher, release, err := d.probeLine(ctx, request.Interface)
	if err != nil {
		return result, err
	}
	defer release()
	if guard, ok := fetcher.(probeFetcher); ok {
		guard.ssid = request.ProbeSSID
		fetcher = guard
		defer func() {
			if err == nil {
				err = guard.check(ctx)
			}
		}()
		if err := guard.check(ctx); err != nil {
			return result, err
		}
	}
	line, ok := fetcher.(auth.Line)
	if !ok || !line.SourceAddr().Is4() || line.SourceAddr().IsUnspecified() {
		return result, domain.Errorf(domain.CodeBindingUnavailable, "没有可用的认证线路绑定")
	}
	gateway, err := auth.ParseGateway(request.ProbeURL, request.ProbeACID)
	if err != nil {
		return result, err
	}
	tx := auth.NewTransaction(line, gateway)
	report(application.PhaseVerify)
	checkCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	identity, err := tx.Online(checkCtx, p.UserID)
	stop()
	if err != nil {
		return result, err
	} // Unknown is not permission to authenticate.
	if identity.Present || request.Kind == application.KindDetectIdentity {
		return identityFinding(identity, p.UserID), nil
	}
	shape := config.EffectiveLogin(config.Defaults(), domain.CampusAccount{Login: p.Login})
	return verifyCandidates(ctx, tx, p, auth.Shape{N: shape.N, Type: shape.Type, Enc: shape.Enc, InfoPrefix: shape.InfoPrefix, OS: shape.OS, Name: shape.Name, DoubleStack: shape.DoubleStack}, report)
}

func verifyCandidates(ctx context.Context, tx *auth.Transaction, p VerificationParams, shape auth.Shape, report func(application.Phase)) (VerificationResult, error) {
	r := VerificationResult{Attempts: []VerificationAttempt{}, Message: "现有认证后缀均未匹配到账号。请核对账号或补充认证后缀。"}
	seen := map[string]bool{}
	for _, suffix := range p.Candidates {
		if seen[suffix] {
			continue
		}
		seen[suffix] = true
		if len(r.Attempts) > 0 {
			timer := time.NewTimer(3500 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return r, ctx.Err()
			}
		}
		username := config.EffectiveUsername(domain.CampusAccount{UserID: p.UserID, OperatorSuffix: suffix})
		attemptCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		report(application.PhaseChallenge)
		challenge, err := tx.Challenge(attemptCtx, username)
		if err != nil {
			stop()
			return r, err
		}
		report(application.PhaseLogin)
		answer, err := tx.Login(attemptCtx, auth.Credentials{Username: username, Password: p.Password}, shape, challenge)
		if err != nil {
			stop()
			return r, err
		}
		outcome, message := classifyVerification(answer)
		if outcome == "hit" || outcome == "online" {
			report(application.PhaseVerify)
			identity, err := tx.Online(attemptCtx, username)
			if err != nil {
				stop()
				return r, err
			}
			// Suffix discovery needs exact identity, unlike maintenance's
			// permissive bare-account comparison. Never log out to find out.
			if identity.Present && identity.Username == username && (outcome == "hit" || strings.Contains(identity.Username, "@")) {
				r.OK, r.Confirmed, r.Suffix = true, true, suffix
				r.PasswordVerified = outcome == "hit"
				r.Source = "login"
				if !r.PasswordVerified {
					r.Source = "online_identity"
				}
				outcome, message = "hit", "已从完整在线账号确认后缀"
			} else {
				outcome, message = "other", "网关未确认候选的完整在线身份，已停止验证"
			}
		}
		stop()
		r.Attempts = append(r.Attempts, VerificationAttempt{suffix, username, outcome, message})
		if outcome != "miss" {
			r.Message = message
			return r, ctx.Err()
		}
	}
	return r, ctx.Err()
}

func classifyVerification(r auth.Result) (string, string) {
	if r.State == domain.AuthAccepted {
		return "hit", "登录请求已被接受"
	}
	if r.AlreadyOnline {
		return "online", "线路已有在线会话"
	}
	text := strings.ToLower(r.GatewayCode + " " + r.GatewayMessage)
	for _, mark := range []string{"e2532", "rate limit", "too frequent", "频繁"} {
		if strings.Contains(text, mark) {
			return "limited", "网关限制了认证频率，请稍后再试"
		}
	}
	for _, mark := range []string{"password error", "bad password", "用户名或密码错误"} {
		if strings.Contains(text, mark) {
			return "credential", "账号或密码错误，已停止验证"
		}
	}
	for _, mark := range []string{"e2531", "user not found", "userid error", "用户不存在"} {
		if strings.Contains(text, mark) {
			return "miss", "账号不存在或认证后缀不匹配"
		}
	}
	return "other", "认证结果无法识别，已停止验证"
}
