package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

// maxDepth bounds nesting. A configuration is three levels deep; anything
// deeper is malformed or hostile, and refusing it early keeps a recursive walk
// from exhausting the stack on a 128 MiB router.
const maxDepth = 24

// scanStrict rejects documents that encoding/json would quietly accept.
//
// Three of these the standard decoder cannot catch at all:
//
//   - Duplicate keys. encoding/json keeps the last one, so
//     {"enabled":true,"enabled":false} silently disables the service.
//   - null. Unmarshalling null into a string or bool leaves the field at its
//     seeded value, so a null looks like "not present" instead of an error.
//   - A second top-level value. The decoder stops after the first, so a file
//     that was appended to instead of replaced would load its stale prefix.
//
// Paths in the errors are the dotted form the UI shows next to a field.
func scanStrict(data []byte) error {
	return scanNullable(data, nil)
}

func scanNullable(data []byte, nullable map[string]bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	scanner := &strictScanner{decoder: decoder, nullable: nullable}
	token, err := decoder.Token()
	if err != nil {
		return domain.Errorf(domain.CodeInvalidConfig, "配置不是有效的 JSON：%v", err).Wrap(err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return domain.Errorf(domain.CodeInvalidConfig, "配置根必须是一个 JSON 对象")
	}
	if err := scanner.object(1); err != nil {
		return err
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return domain.Errorf(domain.CodeInvalidConfig,
			"配置在第一个 JSON 对象之后还有内容；文件可能被追加写入而不是整体替换")
	}
	return nil
}

type strictScanner struct {
	decoder  *json.Decoder
	path     []string
	nullable map[string]bool
}

func (s *strictScanner) location() string {
	if len(s.path) == 0 {
		return ""
	}
	var builder strings.Builder
	for i, part := range s.path {
		if strings.HasPrefix(part, "[") {
			builder.WriteString(part)
			continue
		}
		if i > 0 {
			builder.WriteByte('.')
		}
		builder.WriteString(part)
	}
	return builder.String()
}

func (s *strictScanner) fail(format string, args ...any) error {
	return domain.FieldErrorf(domain.CodeInvalidConfig, s.location(), format, args...)
}

// object consumes tokens up to and including the closing brace. The opening
// brace has already been read.
func (s *strictScanner) object(depth int) error {
	if depth > maxDepth {
		return s.fail("配置嵌套层数超过 %d 层", maxDepth)
	}
	seen := make(map[string]struct{})
	for s.decoder.More() {
		token, err := s.decoder.Token()
		if err != nil {
			return s.fail("读取字段名失败：%v", err)
		}
		key, ok := token.(string)
		if !ok {
			return s.fail("字段名必须是字符串")
		}
		if _, duplicate := seen[key]; duplicate {
			s.path = append(s.path, key)
			err := s.fail("字段重复出现；JSON 解析器只会保留最后一个值，必须拒绝")
			s.path = s.path[:len(s.path)-1]
			return err
		}
		seen[key] = struct{}{}

		s.path = append(s.path, key)
		err = s.value(depth + 1)
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return err
		}
	}
	// Consume '}'.
	if _, err := s.decoder.Token(); err != nil {
		return s.fail("对象未正确结束：%v", err)
	}
	return nil
}

func (s *strictScanner) array(depth int) error {
	if depth > maxDepth {
		return s.fail("配置嵌套层数超过 %d 层", maxDepth)
	}
	index := 0
	for s.decoder.More() {
		s.path = append(s.path, fmt.Sprintf("[%d]", index))
		err := s.value(depth + 1)
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return err
		}
		index++
	}
	if _, err := s.decoder.Token(); err != nil {
		return s.fail("数组未正确结束：%v", err)
	}
	return nil
}

func (s *strictScanner) value(depth int) error {
	token, err := s.decoder.Token()
	if err != nil {
		return s.fail("读取取值失败：%v", err)
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return s.object(depth)
		case '[':
			return s.array(depth)
		default:
			return s.fail("意外的 %q", typed)
		}
	case nil:
		if s.nullable[s.location()] {
			return nil
		}
		return s.fail("不接受 null；缺省请省略该字段，清空请写空字符串")
	default:
		return nil
	}
}
