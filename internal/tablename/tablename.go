// Package tablename 校验表名的合法性。
//
// 单独成一个包，是因为这条规则被四处用到：pkg/tsh 建表、HTTP 路由参数、
// 配置里的 mapping、以及启动时扫描数据目录。
//
// **规则只该有一处实现**：表名会直接拼进文件路径，校验漏一处就是目录穿越。
package tablename

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxLen 是表名长度上限。
	//
	// 64 足够表达业务含义，又能保证文件名远低于各平台的路径长度限制。
	MaxLen = 64

	// Default 是默认表的名字。
	//
	// 现有 API（Engine 上的那些方法、扁平的 HTTP 路由）都作用在它上面，
	// 这样引入表概念不需要改动任何既有调用点。
	Default = "default"
)

// ErrInvalid 表示表名不合法。
var ErrInvalid = errors.New("tablename: 非法表名")

// Validate 校验表名，不合法时返回带原因的 ErrInvalid。
//
// 规则：只允许小写字母、数字、下划线、连字符；首字符必须是字母或数字；
// 长度 1–64。
//
// # 为什么不允许大写
//
// 不是洁癖。**Windows 与 macOS 的文件系统大小写不敏感**，
// `Products.wal` 与 `products.wal` 是同一个文件。允许大写就意味着
// 「Products」和「products」这两张表会共用一份日志、数据互相污染——
// 而这个问题在 Linux 上完全测不出来，等上线才炸。
//
// 与其在三个平台上各写一套判断，不如从源头禁止。
//
// # 为什么首字符不能是下划线或连字符
//
// 它们本身是合法文件名字符，但以下划线开头的名字容易被当成内部/隐藏
// 约定，以连字符开头的名字在命令行里会被当成参数。禁掉省掉一类解释成本。
//
// 注意 `.` 与 `/` `\` 本来就不在字符集里，因此 `..`、`../../etc/passwd`
// 这类路径穿越会被直接拒绝。
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("%w: 不能为空", ErrInvalid)
	}
	if len(name) > MaxLen {
		return fmt.Errorf("%w: %q 长度 %d 超过上限 %d",
			ErrInvalid, name, len(name), MaxLen)
	}

	if strings.ToLower(name) != name {
		// 给出可操作的建议，而不是只说「不允许大写」。
		return fmt.Errorf("%w: %q 含大写字母；"+
			"表名只允许小写（大小写不敏感的文件系统上，Products 与 products "+
			"会指向同一个文件），建议改用 %q",
			ErrInvalid, name, strings.ToLower(name))
	}

	for i, r := range name {
		if !isAllowed(r) {
			return fmt.Errorf("%w: %q 含非法字符 %q；"+
				"只允许小写字母、数字、下划线与连字符，且首字符必须是字母或数字",
				ErrInvalid, name, string(r))
		}
		if i == 0 && (r == '_' || r == '-') {
			return fmt.Errorf("%w: %q 不能以 %q 开头；"+
				"首字符必须是字母或数字", ErrInvalid, name, string(r))
		}
	}

	return nil
}

// IsValid 是 Validate 的布尔版本，便于在条件里直接用。
func IsValid(name string) bool { return Validate(name) == nil }

func isAllowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	default:
		return false
	}
}
