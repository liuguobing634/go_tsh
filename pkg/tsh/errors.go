package tsh

import (
	"github.com/liuguobing/go_tsh/internal/index"
)

// 转发索引层的哨兵错误。
//
// # 为什么要转发
//
// 这些错误都是**调用方能触发的**（ID 重复、字段数超限、字段类型不对……），
// 他们需要 `errors.Is` 做程序化判断。而 `internal/index` 是 internal 包，
// 外部连类型名都写不出来。
//
// 不转发的话，调用方只能去匹配错误文案——那是最脆弱的一种做法：
// 改一个字的措辞就会让下游的 `strings.Contains` 静默失效，
// 而症状是「本来该 409 的变成了 500」。
//
// 这个缺口是写 docs/EMBEDDING.md 时发现的：文档里写了
// `tsh.ErrDocumentExists`，而 `go doc` 里根本没有这个符号。
//
// # 为什么用变量而不是别名
//
// `type A = B` 那种别名是给**类型**用的。错误是**值**：
// `errors.Is` 走的是值的比较（或 Is 方法），所以这里转发的是同一个
// error 值本身，`errors.Is(err, tsh.ErrDocumentExists)` 才成立。
//
// 注意 `ErrNoFields` 不在这里——它有自己的定义（见 typed.go），
// 因为文档层的「一个字段都没有」与索引层的同名错误是两条不同路径。
var (
	// ErrEmptyExternalID 表示文档 ID 为空。
	ErrEmptyExternalID = index.ErrEmptyExternalID

	// ErrEmptyFieldName 表示字段名为空。
	ErrEmptyFieldName = index.ErrEmptyFieldName

	// ErrDocumentExists 表示文档已存在。Create 遇到已存在的 ID 时返回它。
	ErrDocumentExists = index.ErrDocumentExists

	// ErrDocumentNotFound 表示文档不存在。Delete / GetDocument 相关的路径返回它。
	ErrDocumentNotFound = index.ErrDocumentNotFound

	// ErrTooManyFields 表示单文档字段数超出 IndexOptions.MaxFields。
	ErrTooManyFields = index.ErrTooManyFields

	// ErrDocumentTooLarge 表示单文档 token 数超出 IndexOptions.MaxTokens。
	ErrDocumentTooLarge = index.ErrDocumentTooLarge

	// ErrFieldKindConflict 表示同一字段在一份索引里被赋予了两种类型。
	ErrFieldKindConflict = index.ErrFieldKindConflict

	// ErrInvalidFieldKind 表示字段类型名无法识别。
	ErrInvalidFieldKind = index.ErrInvalidFieldKind

	// ErrNotNumericField 表示对非数值/时间字段做了范围或数值操作。
	ErrNotNumericField = index.ErrNotNumericField

	// ErrInvalidFieldValue 表示字段值无法按声明类型解析
	// （例如给 number 字段写了一个不是数字的字符串）。
	ErrInvalidFieldValue = index.ErrInvalidFieldValue
)
