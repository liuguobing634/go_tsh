package tsh

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/wal"
)

// documentsLogName 是持久化日志的文件名。
const documentsLogName = "documents.wal"

// ErrPersistenceBroken 表示持久化层已经失败，写请求被拒绝。
//
// 这是一个**降级状态**：一旦日志写不下去（磁盘满、IO 错误……），
// 索引就已经领先于日志。继续接受写只会让分歧越积越大，
// 因此后续写一律快速失败，直到进程重启。
//
// 读请求不受影响——已经建立的索引仍然可用。
var ErrPersistenceBroken = errors.New("tsh: 持久化已降级，拒绝写入")

// persistState 承载引擎的持久化状态。
type persistState struct {
	log *wal.Log

	// replaying 为 true 时钩子不写日志。
	//
	// 重放时索引要走一遍和当初一样的写入路径，但那不是新变更——
	// 再记一遍会让日志无限自我复制。
	replaying atomic.Bool

	// broken 非 nil 表示已经降级，内容是最初的失败原因。
	broken atomic.Pointer[brokenState]

	// scratch 是编码 payload 的复用缓冲。
	//
	// 不需要加锁：钩子由索引在**写锁内**调用，同一时刻只有一个调用者。
	// 这是本文件里唯一依赖外部同步的地方，改动索引的调用时机时要一并检查。
	scratch []byte
}

type brokenState struct{ err error }

// check 在写路径的最前面调用，让降级后的请求快速失败。
func (p *persistState) check() error {
	if p == nil {
		return nil
	}
	if b := p.broken.Load(); b != nil {
		return fmt.Errorf("%w: %w", ErrPersistenceBroken, b.err)
	}
	return nil
}

// degrade 记录降级原因。只保留第一次失败——那才是根因，
// 后面往往是同一个原因的连锁反应。
func (p *persistState) degrade(err error) {
	p.broken.CompareAndSwap(nil, &brokenState{err: err})
}

// apply 是挂到索引上的变更钩子，在索引写锁内被调用。
func (p *persistState) apply(c index.Change) error {
	if p.replaying.Load() {
		return nil
	}

	if err := p.check(); err != nil {
		return err
	}

	var (
		kind    wal.Kind
		payload []byte
	)

	if c.Deleted {
		kind = wal.KindDelete
		payload = wal.EncodeDelete(c.External, p.scratch[:0])
	} else {
		kind = wal.KindUpsert
		// 字段类型必须落盘，否则重启重放时 number/date 会退化成 text，
		// 动态映射的结果就和重启前不一致了——症状是「重启后范围查询
		// 报字段未声明」，很难联想到根因。
		payload = wal.EncodeUpsert(c.External, c.Fields, kindBytes(c.Kinds), p.scratch[:0])
	}
	p.scratch = payload

	if err := p.log.Append(kind, payload); err != nil {
		// 索引已经改完了，这里无法回滚。把状态标成降级，
		// 让后续写直接失败，避免分歧继续扩大。
		p.degrade(err)
		return err
	}
	return nil
}

// kindBytes 把索引的字段类型转成日志里的编码。
//
// 这里做一次转换而不是让 wal 直接引用 index.FieldKind：
// wal 是通用追加日志，不该知道「文档字段类型」这种业务概念。
// 两边取值的对应关系由 TestFieldKindEncodingMatchesIndex 守着。
func kindBytes(kinds map[string]index.FieldKind) map[string]uint8 {
	if len(kinds) == 0 {
		return nil
	}

	out := make(map[string]uint8, len(kinds))
	for name, k := range kinds {
		out[name] = uint8(k)
	}
	return out
}

// declareKinds 按日志里记录的类型声明字段。
//
// 未列出的字段是 text（默认值，日志里不逐个记录）。
func declareKinds(idx *index.InvertedIndex, kinds map[string]uint8) error {
	for name, raw := range kinds {
		if err := idx.DeclareField(name, index.FieldKind(raw)); err != nil {
			return err
		}
	}
	return nil
}

// replay 把日志里的记录重放回索引。
//
// 语义上刻意做得**宽容**，因为日志只记录「已经生效过的结果」：
//   - upsert 一律按 Upsert 处理，不区分当初是新建还是覆盖；
//   - delete 遇到文档不存在就跳过——可能是「索引改完但日志没写成」
//     那次失败留下的残影，也可能是重复删除；
//   - 但真正的问题（记录解析失败、文档违反当前配置的上限）必须**报错中断**，
//     静默跳过等于无声地丢数据。
func (p *persistState) replay(idx *index.InvertedIndex, logger *slog.Logger) error {
	p.replaying.Store(true)
	defer p.replaying.Store(false)

	var (
		upserts  int
		deletes  int
		skipped  int
		recordNo int
	)

	err := p.log.Replay(func(k wal.Kind, payload []byte) error {
		recordNo++

		switch k {
		case wal.KindUpsert:
			// 版本从**日志本身**取，不用编译期常量：读到旧日志时布局不同，
			// 按新布局解析会读出一堆乱码。
			doc, err := wal.DecodeUpsert(p.log.Version(), payload)
			if err != nil {
				return fmt.Errorf("第 %d 条记录（upsert）解析失败: %w", recordNo, err)
			}

			// 先把类型声明回去，再写文档。
			// 动态映射就靠这一步复现：重放顺序与当初的写入顺序一致，
			// 因此推出的类型表也一致。
			if err := declareKinds(idx, doc.Kinds); err != nil {
				return fmt.Errorf("第 %d 条记录重放文档 %q 的字段类型失败: %w",
					recordNo, doc.External, err)
			}

			if _, _, err := idx.Upsert(doc.External, doc.Fields); err != nil {
				return fmt.Errorf("第 %d 条记录重放文档 %q 失败: %w"+
					"（若是配置上限收紧所致，请调回原来的值或先清理该文档）",
					recordNo, doc.External, err)
			}
			upserts++

		case wal.KindDelete:
			external, err := wal.DecodeDelete(payload)
			if err != nil {
				return fmt.Errorf("第 %d 条记录（delete）解析失败: %w", recordNo, err)
			}
			if err := idx.Delete(external); err != nil {
				if !errors.Is(err, index.ErrDocumentNotFound) {
					return fmt.Errorf("第 %d 条记录删除 %q 失败: %w", recordNo, external, err)
				}
				skipped++
			}
			deletes++

		default:
			return fmt.Errorf("第 %d 条记录的类型 %d 无法识别", recordNo, k)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if logger != nil && (upserts > 0 || deletes > 0) {
		logger.Info("持久化日志重放完成",
			"upserts", upserts, "deletes", deletes,
			"skipped_deletes", skipped, "log_bytes", p.log.Size())
	}
	return nil
}

// openPersist 打开持久化日志。dataDir 为空时返回 (nil, nil)，
// 表示不持久化——此时引擎的行为与纯内存版本完全一致。
func openPersist(dataDir string, syncInterval time.Duration, logger *slog.Logger) (*persistState, error) {
	if dataDir == "" {
		return nil, nil
	}

	log, err := wal.Open(filepath.Join(dataDir, documentsLogName), wal.Options{
		SyncInterval: syncInterval,
		Logger:       logger,
	})
	if err != nil {
		return nil, fmt.Errorf("tsh: 打开持久化日志失败: %w", err)
	}

	return &persistState{log: log}, nil
}
