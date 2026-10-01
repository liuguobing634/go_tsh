package index

import (
	"errors"
	"slices"
)

// DocID 是索引内部使用的稠密文档编号，从 1 开始递增。
//
// 用内部稠密 ID 而不是外部字符串 ID 作为 posting 的主键，
// 是因为求交/求并需要频繁比较与排序：uint32 的归并远快于字符串比较，
// 且 posting 体积可以保持不变（外部 ID 只在文档表里存一份）。
//
// 0 保留为「无效」，便于零值判断。
type DocID uint32

// InvalidDocID 是 DocID 的零值，表示无效文档。
const InvalidDocID DocID = 0

// Document 是索引中保存的文档。
type Document struct {
	// ID 是索引内部编号，对外没有意义，因此不参与 JSON 序列化。
	ID DocID `json:"-"`

	// External 是调用方传入的业务 ID，也是对外唯一的文档标识。
	External string `json:"id"`

	// Fields 是原始字段内容，删除时靠它重新分析以定位 posting。
	Fields map[string]string `json:"fields"`

	// FieldLen 记录每个字段的 token 数，BM25 的长度归一化要用。
	FieldLen map[string]int `json:"-"`

	// TotalLen 是所有字段 token 数之和。
	TotalLen int `json:"-"`
}

// Clone 返回文档的深拷贝。
//
// 索引对外一律返回副本：否则调用方可以在索引锁之外修改内部状态，
// 产生难以复现的数据竞争与脏数据。
func (d *Document) Clone() *Document {
	if d == nil {
		return nil
	}

	c := *d
	c.Fields = make(map[string]string, len(d.Fields))
	for k, v := range d.Fields {
		c.Fields[k] = v
	}
	c.FieldLen = make(map[string]int, len(d.FieldLen))
	for k, v := range d.FieldLen {
		c.FieldLen[k] = v
	}
	return &c
}

// Posting 是「某个词条在某篇文档中出现」的记录。
type Posting struct {
	DocID DocID `json:"doc_id"`

	// TF 是该词条在这篇文档这个字段里的出现次数。
	TF uint32 `json:"tf"`

	// Positions 是各次出现的位置，严格递增，短语查询靠它判定相邻。
	Positions []uint32 `json:"positions"`
}

// PostingList 是某个 (字段, 词条) 的全部出现记录。
//
// Postings 按 DocID 严格升序：DocID 单调递增且删除会整条摘除，
// 因此插入时直接 append 即可维持有序，求交/求并得以走双指针归并。
type PostingList struct {
	Postings []Posting `json:"postings"`
	DF       uint32    `json:"df"`
}

// indexOf 在有序 posting 列表中二分查找指定 DocID。
func (pl *PostingList) indexOf(id DocID) (int, bool) {
	return slices.BinarySearchFunc(pl.Postings, id, func(p Posting, target DocID) int {
		switch {
		case p.DocID < target:
			return -1
		case p.DocID > target:
			return 1
		default:
			return 0
		}
	})
}

// Stats 描述索引的当前规模。
type Stats struct {
	// Docs 是未删除的文档数。
	Docs int `json:"docs"`
	// Terms 是去重后的 (字段, 词条) 组合数。
	Terms int `json:"terms"`
	// Fields 是出现过的字段名数量。
	Fields int `json:"fields"`
	// TotalTokens 是所有文档所有字段的 token 总数。
	TotalTokens uint64 `json:"total_tokens"`
	// AvgDocLen 是平均文档长度（token 数）。
	AvgDocLen float64 `json:"avg_doc_len"`
	// IndexBytes 是索引常驻内存的粗略估算，见 estimateBytesLocked。
	IndexBytes int64 `json:"index_bytes"`
}

// FieldStats 是单个字段的统计量，BM25 的 IDF 与长度归一化都要用。
//
// 注意 IDF 必须按字段计算：同一个词在 title 与 body 中的稀有程度
// 完全不同，混在一起算会让短字段的高信息量被长字段稀释。
type FieldStats struct {
	Field       string  `json:"field"`
	Docs        int     `json:"docs"`
	TotalTokens uint64  `json:"total_tokens"`
	AvgLength   float64 `json:"avg_length"`
}

// 索引返回的哨兵错误，调用方用 errors.Is 判断。
var (
	ErrEmptyExternalID  = errors.New("index: 文档外部 ID 不能为空")
	ErrEmptyFieldName   = errors.New("index: 字段名不能为空")
	ErrDocumentExists   = errors.New("index: 文档已存在")
	ErrDocumentNotFound = errors.New("index: 文档不存在")
	ErrNoFields         = errors.New("index: 文档至少需要一个字段")
	ErrTooManyFields    = errors.New("index: 字段数超出上限")
	ErrDocumentTooLarge = errors.New("index: 文档 token 数超出上限")

	// ErrFieldKindConflict 表示同一字段被赋予了两种类型。
	//
	// 这是**必须报错**的情况而不是可以宽容处理的情况：
	// 一个字段既是数字又是文本时，索引与查询都不知道该按哪套走，
	// 静默选一个只会把问题推迟到更难追查的地方。
	ErrFieldKindConflict = errors.New("index: 字段类型冲突")

	// ErrInvalidFieldKind 表示类型名无法识别。
	ErrInvalidFieldKind = errors.New("index: 非法的字段类型")

	// ErrNotNumericField 表示对非数值字段做了数值/范围操作。
	ErrNotNumericField = errors.New("index: 字段不是数值或时间类型")

	// ErrInvalidFieldValue 表示字段值无法按声明类型解析。
	ErrInvalidFieldValue = errors.New("index: 字段值无法按声明类型解析")
)
