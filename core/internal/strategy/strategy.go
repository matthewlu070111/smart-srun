// Package strategy holds what differs between schools, declared rather than
// coded.
//
// The shape of this package is a reaction to how the baseline grew: a module
// per school, each free to do anything, so adding a school meant writing code
// and every school was a place a bug could hide. Spec 07 asks for the opposite
// -- one built-in strategy that covers the general case, and a declarative
// extension surface that proves a new school does not need code.
//
// Nothing here performs I/O. A strategy describes parameters, declares the
// extra fields its interface should offer, and may name extension commands; it
// cannot open a socket, and the architecture test enforces that. A strategy
// that genuinely needs a request the transaction does not make is a reason to
// widen the interface it is handed, not to build a second HTTP client.
package strategy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// ReservedCommands are the CLI verbs a strategy may never take over.
//
// A school that could claim "login" would change what the core command means on
// that school's routers only, which makes every instruction and every support
// answer conditional on which school is configured.
//
// This is the canonical list and cli.CoreCommands returns it. It used to be
// duplicated there with two extra entries, under a comment saying the two could
// not disagree -- they did, and the gap was real: "service" and "version" are
// commands this build dispatches, so a strategy claiming either would have
// shadowed a working verb. The list lives in this package because cli may
// import strategy and strategy may not import cli.
//
// It is a superset of the sixteen verbs CLAUDE.md lists, which predates the
// service and version commands.
var ReservedCommands = []string{
	"status", "login", "logout", "relogin", "daemon", "schools", "config",
	"switch", "log", "enable", "disable", "help", "man", "update", "presets",
	"detect", "service", "version",
}

// FieldKind is the type of a declared extra field.
//
// The set is closed: these are the controls the interface knows how to render
// and the configuration knows how to validate. A strategy asking for something
// else is asking for interface work, which is a decision, not a declaration.
type FieldKind string

const (
	FieldString FieldKind = "string"
	FieldBool   FieldKind = "bool"
	FieldNumber FieldKind = "number"
	FieldSelect FieldKind = "select"
	FieldMulti  FieldKind = "multi"
)

var fieldKinds = []FieldKind{FieldString, FieldBool, FieldNumber, FieldSelect,
	FieldMulti}

// Field is one school-private setting.
//
// It is what makes school_extra safe: configuration keeps only the keys the
// selected strategy declares, so switching strategies cannot carry another
// one's private values along, and an unknown key is dropped with a diagnosis
// rather than persisted forever.
type Field struct {
	Key   string
	Label string
	Kind  FieldKind
	// Options are required for select and multi, and meaningless otherwise.
	Options []Option
	// Help is shown under the control. Written for a user.
	Help string
}

// Option is one choice in a select or multi field.
type Option struct {
	Value string
	Label string
}

// Command is an extra CLI verb a strategy offers.
type Command struct {
	Name string
	// Summary is the one line the help lists. Chinese, like the rest of the
	// interface.
	Summary string
}

// Strategy is everything a school declares.
type Strategy struct {
	// ID is how configuration refers to this strategy. It is not a preset
	// identifier: spec 03 keeps those separate so that refreshing the public
	// catalogue cannot change which code path a router uses.
	ID string
	// Label is what a person sees in the picker.
	Label string
	// Fields are the school-private settings this strategy understands.
	Fields []Field
	// Commands are the extra verbs it offers.
	Commands []Command
}

// Registry holds the strategies this build knows about.
//
// Static, and populated at startup by the packages that declare strategies.
// There is no plugin loading and no reflection: spec 02 rules both out, and a
// registry that could gain entries at runtime would make "which code is
// running" unanswerable from the source.
type Registry struct {
	order []string
	items map[string]Strategy
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{items: map[string]Strategy{}}
}

// Register adds a strategy, refusing anything malformed.
//
// The refusal happens here, at registration, which in practice is startup.
// Spec 07 requires that specifically: a strategy with a bad declaration must
// not be discoverable and then fail when somebody selects it, because by then
// the person who can fix it is not the person looking at the error.
func (r *Registry) Register(strategy Strategy) error {
	if err := Validate(strategy); err != nil {
		return err
	}
	if _, clash := r.items[strategy.ID]; clash {
		return domain.Errorf(domain.CodeConflict,
			"策略 %s 已经注册过", strategy.ID)
	}
	r.items[strategy.ID] = strategy
	r.order = append(r.order, strategy.ID)
	return nil
}

// MustRegister is for the built-in strategies, which are compiled in.
//
// A malformed built-in is a mistake in this repository, not a configuration
// problem, and it should stop the program at startup rather than produce a
// router that runs with one strategy silently missing.
func (r *Registry) MustRegister(strategy Strategy) {
	if err := r.Register(strategy); err != nil {
		panic(fmt.Sprintf("strategy: built-in %q is not registrable: %v",
			strategy.ID, err))
	}
}

// Lookup finds a strategy by ID.
func (r *Registry) Lookup(id string) (Strategy, bool) {
	item, ok := r.items[strings.TrimSpace(id)]
	return item, ok
}

// List returns the strategies in registration order, which is the order the
// picker shows and is therefore stable rather than map-random.
func (r *Registry) List() []Strategy {
	out := make([]Strategy, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.items[id])
	}
	return out
}

// Validate checks one declaration.
//
// Everything it refuses is something that would otherwise become a runtime
// surprise: a duplicate key that silently wins, a select with nothing to
// select, a command that shadows a core verb.
func Validate(strategy Strategy) error {
	problems := &domain.Errors{Code: domain.CodeInvalidConfig}

	if !isIdentifier(strategy.ID) {
		problems.Addf("id", "策略 id %q 只能包含字母、数字、下划线和连字符",
			strategy.ID)
	}
	if strings.TrimSpace(strategy.Label) == "" {
		problems.Addf("label", "策略 %s 缺少显示名称", strategy.ID)
	}

	seenField := map[string]struct{}{}
	for index, field := range strategy.Fields {
		where := fmt.Sprintf("fields[%d]", index)
		if !isIdentifier(field.Key) {
			problems.Addf(where+".key", "字段名 %q 只能包含字母、数字、下划线和连字符",
				field.Key)
		}
		if _, clash := seenField[field.Key]; clash {
			// Two fields with one key means one of them can never be read
			// back, and which one wins depends on iteration order.
			problems.Addf(where+".key", "字段 %s 重复声明", field.Key)
		}
		seenField[field.Key] = struct{}{}

		if !slices.Contains(fieldKinds, field.Kind) {
			problems.Addf(where+".kind", "字段 %s 的类型 %q 不是界面支持的类型",
				field.Key, field.Kind)
		}
		needsOptions := field.Kind == FieldSelect || field.Kind == FieldMulti
		if needsOptions && len(field.Options) == 0 {
			problems.Addf(where+".options", "字段 %s 是选择型，但没有给出选项",
				field.Key)
		}
		if !needsOptions && len(field.Options) > 0 {
			problems.Addf(where+".options", "字段 %s 不是选择型，不应带选项",
				field.Key)
		}
		seenOption := map[string]struct{}{}
		for _, option := range field.Options {
			if _, clash := seenOption[option.Value]; clash {
				problems.Addf(where+".options", "字段 %s 的选项 %q 重复",
					field.Key, option.Value)
			}
			seenOption[option.Value] = struct{}{}
		}
	}

	seenCommand := map[string]struct{}{}
	for index, command := range strategy.Commands {
		where := fmt.Sprintf("commands[%d]", index)
		name := strings.TrimSpace(command.Name)
		if !isIdentifier(name) {
			problems.Addf(where+".name", "命令名 %q 只能包含字母、数字、下划线和连字符",
				command.Name)
		}
		if slices.Contains(ReservedCommands, name) {
			problems.Addf(where+".name",
				"命令 %s 是核心保留命令，策略不能占用", name)
		}
		if _, clash := seenCommand[name]; clash {
			problems.Addf(where+".name", "命令 %s 重复声明", name)
		}
		seenCommand[name] = struct{}{}
		if strings.TrimSpace(command.Summary) == "" {
			problems.Addf(where+".summary", "命令 %s 缺少说明", name)
		}
	}

	return problems.Err()
}

// DeclaresField reports whether a strategy owns a school_extra key.
//
// This is what configuration uses to decide which private values to keep. A key
// no strategy declares is dropped, which is what stops one school's parameters
// surviving a switch to another.
func (s Strategy) DeclaresField(key string) bool {
	return slices.ContainsFunc(s.Fields, func(field Field) bool {
		return field.Key == key
	})
}

// isIdentifier is the character set for anything that becomes a key, an ID or a
// command name: things that end up in JSON, in a URL and on a command line.
func isIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		symbol := value[index]
		valid := (symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9') ||
			symbol == '_' || symbol == '-'
		if !valid {
			return false
		}
	}
	return true
}
