package portal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	html5 "golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

type Operator struct {
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
}
type OperatorsResult struct {
	OK        bool       `json:"ok"`
	Operators []Operator `json:"operators"`
	SourceURL string     `json:"source_url"`
	Message   string     `json:"message"`
}

var encodedSuffix = regexp.MustCompile(`^(?:[0-9]+\s*-\s*)?@([A-Za-z0-9_.-]{1,128})$`)
var bareSuffix = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// ReadOperators reads actual selector options. Tokenization decodes entities
// exactly once; scripts and arbitrary @ text are never operator evidence.
func ReadOperators(body []byte, contentType string) ([]Operator, error) {
	decoded, err := decodeHTML(body, contentType)
	if err != nil {
		return nil, err
	}
	return readOptions(decoded)
}

func decodeHTML(body []byte, contentType string) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}
	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return nil, domain.Errorf(domain.CodeProtocolInvalid, "认证页编码无法读取")
	}
	return transport.ReadBounded(reader, transport.MaxPortalBody, "解码后的认证页")
}

func readOptions(decoded []byte) ([]Operator, error) {
	z := html5.NewTokenizer(bytes.NewReader(decoded))
	z.SetMaxBuf(transport.MaxPortalBody)
	options := []Operator{}
	selector, value, label := "", "", ""
	active := false
	seen := map[string]bool{}
	finish := func() {
		if !active {
			return
		}
		active = false
		value = strings.TrimSpace(value)
		label = strings.Join(strings.Fields(label), " ")
		for _, placeholder := range []string{"请选择", "select", "choose"} {
			if strings.Contains(strings.ToLower(label), placeholder) {
				return
			}
		}
		var suffix string
		if match := encodedSuffix.FindStringSubmatch(value); match != nil {
			suffix = match[1]
		} else if selector == "operator" || selector == "service" {
			return
		} else if bareSuffix.MatchString(value) {
			suffix = value
		} else if value != "" || label == "" {
			return
		}
		if seen[suffix] || len(options) == 20 {
			return
		}
		seen[suffix] = true
		if utf8.RuneCountInString(label) > 80 {
			label = string([]rune(label)[:80])
		}
		if label == "" {
			label = suffix
		}
		if label == "" {
			label = "不加后缀"
		}
		options = append(options, Operator{suffix, label})
	}
	for {
		tt := z.Next()
		if tt == html5.ErrorToken {
			if z.Err() != io.EOF {
				return nil, domain.Errorf(domain.CodeProtocolInvalid, "认证页结构无法读取")
			}
			finish()
			return options, nil
		}
		t := z.Token()
		switch tt {
		case html5.StartTagToken, html5.SelfClosingTagToken:
			if t.Data == "select" {
				finish()
				selector = ""
				for _, a := range t.Attr {
					if a.Key == "name" || a.Key == "id" {
						switch strings.ToLower(a.Val) {
						case "domain", "realm", "suffix", "operator_suffix", "operator", "service":
							selector = strings.ToLower(a.Val)
						}
					}
				}
			} else if t.Data == "option" {
				finish()
				value, label = "", ""
				hasValue, disabled := false, false
				for _, a := range t.Attr {
					if a.Key == "value" {
						value, hasValue = a.Val, true
					}
					if a.Key == "disabled" {
						disabled = true
					}
				}
				active = selector != "" && hasValue && !disabled
			}
		case html5.EndTagToken:
			if t.Data == "select" || t.Data == "option" {
				finish()
			}
			if t.Data == "select" {
				selector = ""
			}
		case html5.TextToken:
			if active && len(label) < 2048 {
				label += t.Data
			}
		}
	}
}

func operatorAddress(raw string) string {
	address := Address(raw)
	if address == "" {
		return ""
	}
	u, _ := url.Parse(address)
	path := strings.ToLower(u.Path)
	if strings.Contains(path, "/cgi-bin/") || strings.Contains(path, "logout") {
		return ""
	}
	for key := range u.Query() {
		if strings.EqualFold(key, "action") {
			return ""
		}
	}
	return address
}

func sameOrigin(a, b string) bool {
	x, ex := url.Parse(a)
	y, ey := url.Parse(b)
	if ex != nil || ey != nil {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return x.Scheme == y.Scheme && strings.EqualFold(x.Hostname(), y.Hostname()) && port(x) == port(y)
}

// ProbeOperators never stops merely because a URL contains ac_id: the page
// after that redirect is precisely where most gateways keep their selectors.
func ProbeOperators(ctx context.Context, client Fetcher, start, acid string) (OperatorsResult, error) {
	r := OperatorsResult{Operators: []Operator{}, Message: "未识别到认证后缀。"}
	current := operatorAddress(start)
	if current == "" {
		return r, domain.FieldErrorf(domain.CodeInvalidArgument, "base_url", "需要认证登录页地址，不能使用认证动作地址")
	}
	origin := current
	u, _ := url.Parse(current)
	if acid != "" && !u.Query().Has("ac_id") && !u.Query().Has("acid") {
		q := u.Query()
		q.Set("ac_id", acid)
		u.RawQuery = q.Encode()
		current = u.String()
	}
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	seen := map[string]bool{}
	for range MaxRedirects {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if current == "" || !sameOrigin(origin, current) {
			r.Message = "认证页跳转到其它服务，请填写最终登录页地址后重新读取后缀"
			break
		}
		if seen[current] {
			break
		}
		seen[current] = true
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		response, err := client.Do(req)
		if err != nil {
			return r, err
		}
		body, err := transport.ReadBounded(response.Body, transport.MaxPortalBody, "认证页面")
		response.Body.Close()
		if err != nil {
			return r, err
		}
		if response.StatusCode >= 400 {
			r.Message = "认证页读取失败，请检查认证地址"
			break
		}
		body, err = decodeHTML(body, response.Header.Get("Content-Type"))
		if err != nil {
			return r, err
		}
		ops, err := readOptions(body)
		if err != nil {
			return r, err
		}
		if len(ops) > 0 {
			r.OK, r.Operators, r.SourceURL, r.Message = true, ops, current, "已读取认证后缀。"
			break
		}
		location := response.Header.Get("Location")
		if location == "" {
			location = RedirectFromHTML(body)
		}
		if location == "" {
			break
		}
		current = operatorAddress(Join(current, location))
	}
	return r, ctx.Err()
}
