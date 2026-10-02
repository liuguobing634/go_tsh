package tsh

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/liuguobing/go_tsh/internal/index"
	"github.com/liuguobing/go_tsh/internal/tablename"
	"github.com/liuguobing/go_tsh/internal/wal"
)

const (
	// logExt 是每张表的日志文件扩展名。
	logExt = ".wal"

	// tablesSubdir 是数据目录下存放各表日志的子目录。
	//
	// 用子目录而不是平铺：以后要放表级元数据（配额、统计）时有地方放，
	// 而扫描时也不会把数据目录里的其它东西卷进来。
	tablesSubdir = "tables"

	// legacyDocumentsLogName 是引入表之前的全局日志名。
	//
	// 只用于迁移，新代码不再往这个路径写。
	legacyDocumentsLogName = "documents.wal"
)

// documentsLogName 是旧布局的日志文件名。保留这个名字是为了让既有测试
// 与迁移逻辑共用一处定义。
const documentsLogName = legacyDocumentsLogName

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

	switch {
	case len(c.Declared) > 0:
		// 字段类型声明：让没有文档的表也能把 schema 落盘。
		kind = wal.KindDeclare
		payload = wal.EncodeDeclare(kindBytes(c.Declared), p.scratch[:0])

	case c.Deleted:
		kind = wal.KindDelete
		payload = wal.EncodeDelete(c.External, p.scratch[:0])

	default:
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
		declares int
		skipped  int
		recordNo int
	)

	err := p.log.Replay(func(k wal.Kind, payload []byte) error {
		recordNo++

		switch k {
		case wal.KindDeclare:
			// 字段类型声明。它通常出现在文档之前（建表时写下），
			// 也可能夹在文档之间（动态映射后来才遇到新字段）。
			kinds, err := wal.DecodeDeclare(payload)
			if err != nil {
				return fmt.Errorf("第 %d 条记录（declare）解析失败: %w", recordNo, err)
			}
			// 走 declareKinds 而不是直接调 DeclareField：重放期间
			// 钩子会因为 replaying 标志直接返回，不会把读到的声明
			// 又写回日志。
			if err := declareKinds(idx, kinds); err != nil {
				return fmt.Errorf("第 %d 条记录重放字段类型声明失败: %w", recordNo, err)
			}
			declares++

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

	if logger != nil && (upserts > 0 || deletes > 0 || declares > 0) {
		logger.Info("持久化日志重放完成",
			"upserts", upserts, "deletes", deletes, "declares", declares,
			"skipped_deletes", skipped, "log_bytes", p.log.Size())
	}
	return nil
}

// openPersist 打开某张表的持久化日志。
//
// dataDir 为空时返回 (nil, nil)，表示不持久化——此时引擎的行为与纯内存
// 版本完全一致。
func openPersist(
	dataDir, table string,
	syncInterval time.Duration,
	logger *slog.Logger,
) (*persistState, error) {
	if dataDir == "" {
		return nil, nil
	}

	// 表名在这里再校验一次，而不是只信任调用方。
	//
	// 表名要拼进文件路径，任何一条没经过校验的路径都是目录穿越。
	// 多校验一次的代价是一次几纳秒的字符串扫描。
	if err := tablename.Validate(table); err != nil {
		return nil, err
	}

	dir := tablesDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("tsh: 创建表目录失败: %w", err)
	}

	log, err := wal.Open(filepath.Join(dir, table+logExt), wal.Options{
		SyncInterval: syncInterval,
		Logger:       logger,
	})
	if err != nil {
		return nil, fmt.Errorf("tsh: 打开表 %q 的持久化日志失败: %w", table, err)
	}

	return &persistState{log: log}, nil
}

// tablesDir 返回存放各表日志的目录。
func tablesDir(dataDir string) string { return filepath.Join(dataDir, tablesSubdir) }

// removeTableFile 删除某张表的日志文件。
//
// 文件不存在不算错误：删表是幂等的目标状态，而不是「必须删掉一个东西」。
func removeTableFile(dataDir, table string) error {
	if dataDir == "" {
		return nil
	}
	if err := tablename.Validate(table); err != nil {
		return err
	}

	path := filepath.Join(tablesDir(dataDir), table+logExt)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tsh: 删除表 %q 的日志失败: %w", table, err)
	}
	return nil
}

// discoverTables 扫描数据目录，返回已存在的表名。
//
// 目录里的文件分三类，处理方式不同：
//
//  1. 合法表名 + 本程序的魔数 → 那就是一张表。
//  2. **魔数正确但表名不合法** → **拒绝启动**。
//     那是我们的数据，只是名字被改过。静默跳过等于无声地丢一整个表。
//  3. 没有魔数 → 不是我们的文件，跳过并告警。
//     目录里混进 README、.DS_Store、用户随手拷来的东西都很常见，
//     为此拒绝启动只会让人摸不着头脑。
//
// 只看魔数不看扩展名：扩展名是约定，魔数才是事实。
func discoverTables(dataDir string, logger *slog.Logger) ([]string, error) {
	if dataDir == "" {
		return nil, nil
	}

	dir := tablesDir(dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// 还没建过任何表，正常情况。
			return nil, nil
		}
		return nil, fmt.Errorf("tsh: 读取表目录失败: %w", err)
	}

	var (
		names   []string
		skipped []string
	)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		name := e.Name()
		if !strings.HasSuffix(name, logExt) {
			skipped = append(skipped, name)
			continue
		}

		table := strings.TrimSuffix(name, logExt)
		path := filepath.Join(dir, name)

		ours, err := hasOurMagic(path)
		if err != nil {
			return nil, fmt.Errorf("tsh: 读取 %s 失败: %w", path, err)
		}

		if !ours {
			skipped = append(skipped, name)
			continue
		}

		if err := tablename.Validate(table); err != nil {
			return nil, fmt.Errorf(
				"tsh: %s 是本程序的日志（魔数正确），但文件名不是合法表名：%w。"+
					"这是数据，不能跳过；请改名或移走后再启动", path, err)
		}
		names = append(names, table)
	}

	if logger != nil && len(skipped) > 0 {
		logger.Warn("表目录里有非本程序的文件，已跳过",
			"dir", dir, "files", skipped)
	}

	slices.Sort(names)
	return names, nil
}

// hasOurMagic 判断文件是否是我们写的日志。
func hasOurMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, len(wal.Magic))
	n, err := io.ReadFull(f, buf)
	if err != nil {
		// 空文件或短文件：不是完整的日志，但不是读错误。
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return n == len(wal.Magic) && string(buf) == wal.Magic, nil
}

// migrateLegacyLog 把旧布局的 <dataDir>/documents.wal 迁移到
// <dataDir>/tables/default.wal。
//
// 老版本只有一个全局日志。引入表之后它天然对应默认表。
//
// 只在「旧文件在、新文件不在」时动手，因此是幂等的：
// 迁移过一次之后这个函数就永远走空了。
//
// 用改名而不是「继续读老路径」：两条路径并存意味着两套代码分支，
// 而这是一次性的迁移，做完就干净了。目标文件不存在，所以不涉及
// os.Rename 在 Windows 上的覆盖语义（那条风险更高，这里避开了）。
func migrateLegacyLog(dataDir string, logger *slog.Logger) error {
	if dataDir == "" {
		return nil
	}

	oldPath := filepath.Join(dataDir, documentsLogName)
	newPath := filepath.Join(tablesDir(dataDir), tablename.Default+logExt)

	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 没有旧文件，最常见的情况
		}
		return fmt.Errorf("tsh: 检查旧日志 %s 失败: %w", oldPath, err)
	}
	if oldInfo.IsDir() {
		return nil
	}

	if _, err := os.Stat(newPath); err == nil {
		// 两个都在：说明已经迁移过，而旧文件是用户自己又放回来的。
		// 不动它，但要明确告警——否则用户会以为自己的数据被读了。
		if logger != nil {
			logger.Warn("旧日志与新布局同时存在，已忽略旧文件",
				"old", oldPath, "new", newPath)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("tsh: 检查 %s 失败: %w", newPath, err)
	}

	if err := os.MkdirAll(tablesDir(dataDir), 0o755); err != nil {
		return fmt.Errorf("tsh: 创建表目录失败: %w", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		// 改名失败就不启动。半途而废会让用户既看不到旧数据、
		// 也不知道新数据该往哪写。
		return fmt.Errorf(
			"tsh: 迁移旧日志失败（%s -> %s）: %w；"+
				"请手动改名后重试", oldPath, newPath, err)
	}

	if logger != nil {
		logger.Info("已把旧日志迁移到默认表", "from", oldPath, "to", newPath)
	}
	return nil
}
