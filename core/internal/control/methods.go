package control

import (
	"fmt"
	"slices"
	"strings"
)

// Kind separates calls that only read from calls that change something.
//
// It is not decoration. Read calls are what the LuCI page polls every few
// seconds, so they must answer from cached state: a "read" that authenticated
// or probed the network would turn an idle browser tab into traffic on the
// campus gateway.
type Kind string

const (
	// KindRead answers from state already held. No network, no writes.
	KindRead Kind = "read"
	// KindMutate changes configuration, or queues or cancels work.
	KindMutate Kind = "mutate"
	// KindTask starts bounded background work and returns a handle. The work
	// outliving the request is the point: a browser that navigates away must
	// not cancel an install.
	KindTask Kind = "task"
)

// Method describes one callable name.
type Method struct {
	Name string
	Kind Kind
	// Note records the constraint that is easy to violate later, in the place
	// someone adding a handler will actually read.
	Note string
}

// catalogue is the complete set of method names this protocol will ever accept.
//
// Fixed, not assembled at runtime: a registry that accepted whatever a handler
// registered would make the wire surface depend on initialisation order, and
// "which methods exist" is a security property here, not an implementation
// detail.
var catalogue = []Method{
	{"status.get", KindRead, "一次组合快照；不得触发认证或同步探测"},
	{"schema.get", KindRead, "只读字段契约，供 LuCI 与 CLI 共用一份默认值"},
	{"capabilities.get", KindRead, "本设备实际具备的能力，缺工具报 UnsupportedCapability"},
	{"version.get", KindRead, ""},

	{"config.get", KindRead, "不返回秘密明文"},
	{"config.export", KindRead, "明确 include_secrets 才导出凭据；不得用于状态轮询"},
	{"config.import", KindMutate, "严格校验备份；预览不写盘；提交需要 expected_revision"},
	{"config.validate", KindRead, "纯校验，不写盘"},
	{"config.apply", KindMutate, "必须带 expected_revision；冲突时保留旧配置"},

	{"campus.get", KindRead, "只有明确授权的详情响应才包含密码"},
	{"campus.upsert", KindMutate, "presence 明确：字段省略=保持，字符串=替换"},
	{"campus.remove", KindMutate, "删除后必须修复 active/default 指针"},
	{"campus.set_default", KindMutate, "同时更新同类 active/default 指针"},

	{"hotspot.get", KindRead, ""},
	{"hotspot.upsert", KindMutate, ""},
	{"hotspot.remove", KindMutate, ""},
	{"hotspot.set_default", KindMutate, ""},

	{"action.submit", KindMutate, "queued 不等于 succeeded；重复 key 返回同一任务"},
	{"action.get", KindRead, ""},
	{"action.cancel", KindMutate, "取消不保证网关没收到请求"},

	{"presets.list", KindRead, ""},
	{"presets.refresh", KindTask, "远程刷新任务化，不得阻塞状态轮询"},
	{"user_presets.get", KindRead, ""},
	{"user_presets.set", KindMutate, "独立 revision，CAS 只作用于用户预设文件"},

	{"detect.environment", KindTask, "未选线路时不开始探测"},
	{"detect.acid", KindTask, "完整输入路径先探测再归一化 origin"},
	{"detect.operators", KindTask, "被动获取页面选项；selected 不等于本人运营商"},
	{"detect.identity", KindTask, "只读身份，绝不携带输入的密码"},
	{"detect.verify", KindTask, "唯一可主动认证的探测；候选上限 5"},

	{"setup_wifi.start", KindTask, "job 与当前会话关联"},
	{"setup_wifi.status", KindRead, ""},
	{"setup_wifi.cancel", KindMutate, "幂等"},
	{"setup_wifi.commit", KindMutate, "幂等"},
	{"setup_wifi.account", KindMutate, "用向导结果保存账号"},

	{"log.tail", KindRead, "增量游标；channel=plugin/network"},
	{"log.download", KindRead, "有界读取"},
	{"log.clear", KindMutate, "只清本项目日志，不动系统全局日志"},

	{"update.check", KindTask, ""},
	{"update.start", KindTask, "只接受已验证的 release/plan ID，绝不接受任意 URL 或 shell"},
	{"update.status", KindRead, "主服务停止时也要能从固定路径只读续查"},

	{"schools.list", KindRead, ""},
	{"schools.inspect", KindRead, ""},
	{"school.command", KindMutate, "白名单命令，不能遮蔽核心命令"},
}

var catalogueByName = func() map[string]Method {
	out := make(map[string]Method, len(catalogue))
	for _, method := range catalogue {
		out[method.Name] = method
	}
	return out
}()

// Catalogue returns every declared method, sorted by name.
func Catalogue() []Method {
	out := slices.Clone(catalogue)
	slices.SortFunc(out, func(a, b Method) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Lookup reports the declared method with this name.
func Lookup(name string) (Method, bool) {
	method, ok := catalogueByName[name]
	return method, ok
}

// MethodNames lists every declared name.
func MethodNames() []string {
	out := make([]string, 0, len(catalogue))
	for _, method := range catalogue {
		out = append(out, method.Name)
	}
	slices.Sort(out)
	return out
}

// validateCatalogue reports the first malformed or duplicated entry.
//
// It returns an error rather than panicking so the rules themselves can be
// tested; init turns a failure into a panic, because a protocol surface that is
// wrong at startup must not go on to serve requests.
func validateCatalogue(methods []Method) error {
	seen := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		family, action, found := strings.Cut(method.Name, ".")
		if !found || family == "" || action == "" {
			return fmt.Errorf("control: method %q is not family.action", method.Name)
		}
		switch method.Kind {
		case KindRead, KindMutate, KindTask:
		default:
			return fmt.Errorf("control: method %q has unknown kind %q",
				method.Name, method.Kind)
		}
		if _, duplicate := seen[method.Name]; duplicate {
			return fmt.Errorf("control: duplicate method in catalogue: %s", method.Name)
		}
		seen[method.Name] = struct{}{}
	}
	return nil
}

// The check runs at init so a malformed entry fails the build's first test
// rather than the first request.
func init() {
	if err := validateCatalogue(catalogue); err != nil {
		panic(err.Error())
	}
}
