// Package wal 实现一个只追加的文档日志。
//
// 它刻意做得很窄：只负责「按顺序追加记录」与「按顺序读回来」，
// 不理解记录内容的业务含义，也不碰索引。
//
// # 格式
//
// 文件头（一次）：
//
//	magic   [4]byte   固定为 "TSHW"
//	version uint8     当前为 1
//	_       [3]byte   保留，便于将来扩展而不破坏对齐
//
// 记录（重复）：
//
//	kind    uint8
//	length  uint32    小端，payload 的字节数
//	payload [length]byte
//	crc32   uint32    小端，覆盖 kind + length + payload
//
// 每条记录自带校验和，因此**崩溃时写了一半的尾部记录是可以被识别出来的**，
// 不会污染前面的数据（见 Open 的恢复语义）。
package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// Magic 是文件头的魔数。
	Magic = "TSHW"

	// FormatVersion 是当前写入的格式版本。
	//
	// 版本历史：
	//   - v1：upsert 只有「名字 + 值」
	//   - v2：upsert 每个字段多一个类型字节（动态映射要在重启后复现）
	//   - v3：新增声明字段类型的记录类型（空表的 schema 也要活过重启）
	//
	// v3 并没有改变已有记录的**布局**——它只是多了一种记录类型。
	// 之所以仍然要升版本号：旧二进制读到不认识的类型会把它当成
	// 损坏数据并拒绝启动，那个报错（「日志损坏」）会把人引向完全
	// 错误的方向。升版本号能让它干净地说出「版本不支持」。
	FormatVersion uint8 = 3

	// legacyFormatVersion 是仍然可以**读**的最旧版本。
	//
	// 兼容读取而不是拒绝启动：旧数据完全可解析，
	// 逼用户删数据重来是最差的处理方式。
	legacyFormatVersion uint8 = 1

	headerSize = 8

	// defaultMaxRecordBytes 是单条记录 payload 的默认上限。
	//
	// 它有两个作用：
	//  1. 给损坏的长度字段兜底——length 被写坏成极大值时，
	//     没有上限就会尝试分配几个 GB；
	//  2. 作为尾部截断的安全阈值，见 Open 的恢复语义。
	//
	// 默认值远大于单文档的合理体积（HTTP 请求体上限默认 8 MiB）。
	defaultMaxRecordBytes = 16 << 20
)

// compactSuffix 是压实期间的临时文件后缀。
//
// 压实先写这个文件、再改名覆盖正式文件。因此它**永远只是草稿**：
// 启动时看到它就说明上次压实没走完，直接删掉即可，正式文件仍然完整。
const compactSuffix = ".compact"

// headerBytes 返回一个全新日志文件的文件头。
func headerBytes() []byte {
	h := make([]byte, headerSize)
	copy(h, Magic)
	h[4] = FormatVersion
	return h
}

// EncodeRecord 把一条记录编码成完整字节：kind | length | payload | crc32。
//
// dst 为 nil 时自行分配；传 dst[:0] 可以复用缓冲。
// 校验和覆盖 kind + length + payload——长度字段被写坏也能被发现。
func EncodeRecord(kind Kind, payload []byte, dst []byte) []byte {
	start := len(dst)
	dst = append(dst, byte(kind))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, payload...)
	return binary.LittleEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:]))
}

// Kind 是记录的类型。
type Kind uint8

const (
	// KindUpsert 表示「这个 ID 现在的内容是这些字段」。
	// 新建与覆盖都用它：重放时不需要区分。
	KindUpsert Kind = 1

	// KindDelete 表示删除。
	KindDelete Kind = 2

	// KindDeclare 表示「这些字段的类型是这样」。
	//
	// 它让**没有文档的表**也能把 schema 落盘。
	// 没有它的话，「建表声明好字段类型、再慢慢灌数据」这种最常见的用法
	// 在重启后会丢掉全部类型信息，之后写进去的数据会被按推断的类型
	// 重新解释——数字变成文本，范围查询随之失效。
	KindDeclare Kind = 3
)

// knownKinds 是当前能识别的全部记录类型。
//
// 扫描时用它判断记录是否有效：认不出的类型会让扫描在**该条记录的开头**
// 停下，并被当成文件尾部截断处理。
var knownKinds = [...]Kind{KindUpsert, KindDelete, KindDeclare}

func isKnownKind(k Kind) bool {
	for _, known := range knownKinds {
		if k == known {
			return true
		}
	}
	return false
}

func (k Kind) String() string {
	switch k {
	case KindUpsert:
		return "upsert"
	case KindDelete:
		return "delete"
	case KindDeclare:
		return "declare"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(k))
	}
}

// 日志包的哨兵错误。
var (
	// ErrClosed 表示日志已经关闭。
	ErrClosed = errors.New("wal: 日志已关闭")

	// ErrCorrupted 表示日志在**非尾部**的位置出现损坏。
	//
	// 尾部损坏（崩溃时写了一半）会被自动截断；中间损坏说明数据真的坏了，
	// 这时宁可拒绝启动让人来处理，也不要把后面可能完好的记录一起丢掉。
	ErrCorrupted = errors.New("wal: 日志损坏")

	// ErrFormatMismatch 表示文件头与本程序期望的格式不符。
	ErrFormatMismatch = errors.New("wal: 格式不匹配")
)

// Options 配置日志。
type Options struct {
	// SyncInterval 是批量 fsync 的间隔，<= 0 时取 100ms。
	//
	// 语义（组提交）：
	//   - 进程崩溃：不丢。写入已经交给操作系统，进程死了数据还在。
	//   - 断电 / 宿主机崩溃：最多丢这个间隔内的写。
	SyncInterval time.Duration

	// Logger 用于报告恢复过程中的异常，为 nil 时用 slog.Default()。
	Logger *slog.Logger

	// MaxRecordBytes 是单条记录 payload 的上限，<= 0 时取 16 MiB。
	//
	// 一般不需要调整：它的默认值远大于任何合理的文档体积。
	// 设小会让合法的大文档写不进去，设大则削弱损坏长度字段的兜底能力。
	MaxRecordBytes int
}

func (o Options) withDefaults() Options {
	if o.SyncInterval <= 0 {
		o.SyncInterval = 100 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.MaxRecordBytes <= 0 {
		o.MaxRecordBytes = defaultMaxRecordBytes
	}
	return o
}

// Log 是一个只追加的文档日志。
//
// 并发安全：Append / Sync / Close 可以由多个 goroutine 调用。
type Log struct {
	f      *os.File
	path   string
	logger *slog.Logger

	interval time.Duration
	maxRec   int

	// version 是**这个文件**的格式版本，可能低于 FormatVersion（旧日志）。
	// 构造后只读，不需要加锁。
	version uint8

	mu     sync.Mutex
	buf    []byte // 复用编码缓冲，避免每次追加都分配
	dirty  bool   // 自上次 fsync 以来是否有写入
	closed bool
	size   int64 // 当前文件末尾偏移

	stopSync chan struct{}
	syncDone chan struct{}
	closeOne sync.Once
}

// Open 打开（必要时创建）路径上的日志，并**完成崩溃恢复**。
//
// 恢复语义：
//   - 文件不存在 → 创建并写入文件头；
//   - 文件头 magic 或版本不符 → 返回 ErrFormatMismatch，**不覆盖**已有数据
//     （这里可能是别人的文件，也可能需要人工迁移）；
//   - 正常记录之后跟着**不完整或校验失败**的尾部 → 截断掉并告警。
//     这是崩溃时写了一半的记录，属于预期情况；
//   - 但若尾部损坏之后还跟着超过一条记录大小的数据，说明损坏不在尾部，
//     返回 ErrCorrupted 拒绝启动——绝不静默丢掉一批可能完好的数据。
func Open(path string, opts Options) (*Log, error) {
	opts = opts.withDefaults()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("wal: 创建目录失败: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return nil, fmt.Errorf("wal: 打开日志失败: %w", err)
	}

	l := &Log{
		f:        f,
		path:     path,
		logger:   opts.Logger,
		interval: opts.SyncInterval,
		maxRec:   opts.MaxRecordBytes,
		stopSync: make(chan struct{}),
		syncDone: make(chan struct{}),
	}

	if err := l.recover(); err != nil {
		_ = f.Close()
		return nil, err
	}

	go l.syncLoop()
	return l, nil
}

// recover 校验文件头并修掉崩溃留下的尾部。
func (l *Log) recover() error {
	info, err := l.f.Stat()
	if err != nil {
		return fmt.Errorf("wal: 读取文件状态失败: %w", err)
	}

	switch size := info.Size(); {
	case size == 0:
		// 全新文件：写文件头。
		header := headerBytes()
		if _, err := l.f.Write(header); err != nil {
			return fmt.Errorf("wal: 写入文件头失败: %w", err)
		}
		if err := l.f.Sync(); err != nil {
			return fmt.Errorf("wal: 文件头 fsync 失败: %w", err)
		}
		l.size = headerSize
		return nil

	case size < headerSize:
		return fmt.Errorf("%w: 文件太小（%d 字节），连文件头都不完整",
			ErrFormatMismatch, size)
	}

	header := make([]byte, headerSize)
	if _, err := l.f.ReadAt(header, 0); err != nil {
		return fmt.Errorf("wal: 读取文件头失败: %w", err)
	}
	if string(header[:4]) != Magic {
		return fmt.Errorf("%w: 期望魔数 %q，实际 %q",
			ErrFormatMismatch, Magic, string(header[:4]))
	}
	// 版本必须落在 [legacyFormatVersion, FormatVersion] 区间内。
	//
	// 用区间而不是「等于当前版本或等于最旧版本」——后者在版本多于两个时
	// 会静默地把中间那些版本拒之门外。v2 就是这么被漏掉的：
	// 升到 v3 之后，所有已有的 v2 日志都打不开了。
	if header[4] < legacyFormatVersion || header[4] > FormatVersion {
		return fmt.Errorf("%w: 版本 %d 不在支持范围 [%d, %d] 内",
			ErrFormatMismatch, header[4], legacyFormatVersion, FormatVersion)
	}
	// 记下这份日志的实际版本：解码方必须按它来，而不是按编译期常量。
	l.version = header[4]

	validEnd, badAt, err := l.scan(info.Size())
	if err != nil {
		return err
	}

	if badAt >= 0 {
		tail := info.Size() - badAt
		// 尾部损坏之后还跟着大量数据，说明坏的不是"最后一条"。
		// 这时截断等于把可能完好的记录一起扔掉，宁可拒绝启动。
		if tail > int64(l.maxRec) {
			return fmt.Errorf("%w: 偏移 %d 处损坏，但其后仍有 %d 字节数据，"+
				"这不是崩溃留下的半条记录；请人工检查 %s",
				ErrCorrupted, badAt, tail, l.path)
		}

		l.logger.Warn("日志尾部不完整，已截断（这是崩溃时写了一半的记录）",
			"path", l.path, "kept_bytes", validEnd, "discarded_bytes", tail)

		if err := l.f.Truncate(validEnd); err != nil {
			return fmt.Errorf("wal: 截断损坏尾部失败: %w", err)
		}
		if err := l.f.Sync(); err != nil {
			return fmt.Errorf("wal: 截断后 fsync 失败: %w", err)
		}
	}

	l.size = validEnd
	if _, err := l.f.Seek(validEnd, io.SeekStart); err != nil {
		return fmt.Errorf("wal: 定位到写入位置失败: %w", err)
	}
	return nil
}

// scan 从头扫描到第一条不完整或校验失败的记录。
//
// 返回 (有效数据末尾偏移, 坏记录起始偏移)。badAt 为 -1 表示整个文件都完好。
func (l *Log) scan(size int64) (validEnd, badAt int64, err error) {
	if _, err := l.f.Seek(headerSize, io.SeekStart); err != nil {
		return 0, -1, fmt.Errorf("wal: 定位失败: %w", err)
	}

	r := bufio.NewReaderSize(l.f, 1<<20)
	off := int64(headerSize)

	var (
		prefix  [5]byte
		crcBuf  [4]byte
		payload []byte
	)

	for off < size {
		if _, err := io.ReadFull(r, prefix[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return off, -1, nil // 正好收在记录边界上
			}
			// 半条记录：崩溃时写到这里为止。
			return off, off, nil
		}

		kind := Kind(prefix[0])
		n := binary.LittleEndian.Uint32(prefix[1:5])

		if !isKnownKind(kind) {
			return off, off, nil
		}
		if int64(n) > int64(l.maxRec) {
			return off, off, nil
		}

		if cap(payload) < int(n) {
			payload = make([]byte, n)
		}
		payload = payload[:n]

		if _, err := io.ReadFull(r, payload); err != nil {
			return off, off, nil
		}
		if _, err := io.ReadFull(r, crcBuf[:]); err != nil {
			return off, off, nil
		}

		h := crc32.NewIEEE()
		h.Write(prefix[:])
		h.Write(payload)
		if h.Sum32() != binary.LittleEndian.Uint32(crcBuf[:]) {
			return off, off, nil
		}

		off += int64(len(prefix)) + int64(n) + int64(len(crcBuf))
	}

	return off, -1, nil
}

// Replay 从头按顺序读出全部有效记录。
//
// fn 收到的 payload 底层缓冲会被后续记录复用，**只在本次调用期间有效**；
// 需要留存请自行拷贝。
//
// fn 返回错误时立即停止并原样返回。
func (l *Log) Replay(fn func(Kind, []byte) error) error {
	l.mu.Lock()
	validEnd := l.size
	l.mu.Unlock()

	if _, err := l.f.Seek(headerSize, io.SeekStart); err != nil {
		return fmt.Errorf("wal: 重放定位失败: %w", err)
	}

	r := bufio.NewReaderSize(l.f, 1<<20)
	off := int64(headerSize)

	var (
		prefix  [5]byte
		crcBuf  [4]byte
		payload []byte
	)

	for off < validEnd {
		if _, err := io.ReadFull(r, prefix[:]); err != nil {
			return fmt.Errorf("wal: 重放到偏移 %d 时读取失败: %w", off, err)
		}

		n := binary.LittleEndian.Uint32(prefix[1:5])
		if cap(payload) < int(n) {
			payload = make([]byte, n)
		}
		payload = payload[:n]

		if _, err := io.ReadFull(r, payload); err != nil {
			return fmt.Errorf("wal: 重放到偏移 %d 时读取 payload 失败: %w", off, err)
		}
		if _, err := io.ReadFull(r, crcBuf[:]); err != nil {
			return fmt.Errorf("wal: 重放到偏移 %d 时读取校验和失败: %w", off, err)
		}

		if err := fn(Kind(prefix[0]), payload); err != nil {
			return err
		}
		off += int64(len(prefix)) + int64(n) + int64(len(crcBuf))
	}

	return nil
}

// Append 追加一条记录。
//
// 返回时数据已经交给操作系统：**进程崩溃不会丢**。
// 断电安全则要等下一次 fsync（默认 100ms 一次），这是批量策略的取舍。
//
// 返回值 == nil 表示这次写已经进入日志；调用方可以据此对客户端确认。
func (l *Log) Append(kind Kind, payload []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrClosed
	}

	// 必须和 scan 用同一个上限，否则自己写进去的记录下次打开会被
	// 当成损坏数据截掉——那是最糟的一类 bug：能写、但读不回来。
	if len(payload) > l.maxRec {
		return fmt.Errorf("wal: 记录过大: %d 字节 > 上限 %d", len(payload), l.maxRec)
	}

	buf := EncodeRecord(kind, payload, l.buf[:0])
	l.buf = buf

	if _, err := l.f.Write(buf); err != nil {
		return fmt.Errorf("wal: 追加记录失败: %w", err)
	}

	l.size += int64(len(buf))
	l.dirty = true
	return nil
}

// Sync 立刻把已写入的字节刷到磁盘。
func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.syncLocked()
}

func (l *Log) syncLocked() error {
	if !l.dirty || l.closed {
		return nil
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("wal: fsync 失败: %w", err)
	}
	l.dirty = false
	return nil
}

func (l *Log) syncLoop() {
	defer close(l.syncDone)

	t := time.NewTicker(l.interval)
	defer t.Stop()

	for {
		select {
		case <-l.stopSync:
			return
		case <-t.C:
			if err := l.Sync(); err != nil {
				// 刷盘失败不能装作没事：数据还在页缓存里，
				// 但断电就会丢。交给日志系统报警。
				l.logger.Error("日志 fsync 失败", "err", err, "path", l.path)
			}
		}
	}
}

// Close 停止后台刷盘、做最后一次 fsync 并关闭文件。可重复调用。
func (l *Log) Close() error {
	l.closeOne.Do(func() { close(l.stopSync) })
	<-l.syncDone

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true

	syncErr := l.syncLocked()
	closeErr := l.f.Close()

	switch {
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return fmt.Errorf("wal: 关闭日志失败: %w", closeErr)
	}
	return nil
}

// Size 返回日志当前的字节数（含文件头）。
func (l *Log) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// Compact 用 fn 产出的记录重写日志文件。
//
// # 为什么需要
//
// 日志是只追加的，记录的是「历史上发生过的每一次写」。同一篇文档更新
// 10 次就有 11 条记录，重启时要把这 11 条全部重放一遍。实测 5000 篇
// 各更新 10 次之后：日志是存活数据的 **11.6 倍**，重启耗时是
// 「从不更新」时的 **34 倍**（详情见 docs/PERFORMANCE.md）。
//
// 压实把日志重写成「当前状态的快照」，两个数字都回到 1 倍附近。
//
// # mark 是什么
//
// 调用方捕获「基线状态」时的日志长度。fn 执行期间新产生的写会追加到
// 旧文件上，Compact 会把 [mark, 当前长度) 的原始字节**接在新内容之后**，
// 于是新日志 = 基线 + 增量 = 当前状态。
//
// mark 必须 ≤ 捕获基线状态时的实际偏移：
//   - 偏大 → 那段记录两边都没有，**丢数据**；
//   - 偏小 → 那段记录两边都有，只是多写一遍（幂等，无害）。
//
// 正确做法是在**同一把读锁内**同时捕获状态与 Size()。
//
// # 锁的边界
//
// fn 序列化时**不持锁**——它可能要写几百 MB，锁住等于把所有写请求停住
// 好几秒。只有「接增量 + fsync + 改名」这一段持锁，且与索引大小无关。
func (l *Log) Compact(mark int64, fn func(emit func(Kind, []byte) error) error) error {
	if mark < headerSize {
		return fmt.Errorf("wal: 压实的基线偏移 %d 不合法（至少要越过 %d 字节的文件头）",
			mark, headerSize)
	}
	if fn == nil {
		return fmt.Errorf("wal: 压实必须提供记录产出函数")
	}

	compactPath := l.path + compactSuffix

	// ---- 第 1 步：不持锁，把新内容写出来 ----
	f, err := os.Create(compactPath)
	if err != nil {
		return fmt.Errorf("wal: 创建压实临时文件失败: %w", err)
	}

	abort := func(cause error) error {
		_ = f.Close()
		_ = os.Remove(compactPath)
		return cause
	}

	if _, err := f.Write(headerBytes()); err != nil {
		return abort(fmt.Errorf("wal: 写压实文件头失败: %w", err))
	}

	// 用局部 scratch 而不是 l.buf：l.buf 归 Append 用，
	// 这段代码不持锁，共用它会数据竞争。
	var scratch []byte
	emit := func(kind Kind, payload []byte) error {
		scratch = EncodeRecord(kind, payload, scratch[:0])
		if _, err := f.Write(scratch); err != nil {
			return fmt.Errorf("wal: 写压实记录失败: %w", err)
		}
		return nil
	}

	if err := fn(emit); err != nil {
		return abort(err)
	}

	// ---- 第 2 步：短锁内接增量并替换 ----
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return abort(ErrClosed)
	}

	if err := l.appendDeltaLocked(f, mark); err != nil {
		return abort(err)
	}
	if err := f.Sync(); err != nil {
		return abort(fmt.Errorf("wal: 压实文件 fsync 失败: %w", err))
	}
	if err := f.Close(); err != nil {
		return abort(fmt.Errorf("wal: 关闭压实文件失败: %w", err))
	}

	// 此刻新文件已经完整落盘，而旧文件**一个字都还没动**。
	// 从这里到改名成功之间无论怎么崩，旧日志都是完整的。

	// Windows 上目标文件还有打开的句柄时改名会失败（实测 Access is denied），
	// 所以必须先关掉旧文件。
	//
	// 先把 dirty 清掉：后台 syncLoop 也走 l.mu，虽然此刻被我们挡在外面，
	// 但清掉它可以让「句柄为空」这个窗口里任何 sync 路径都直接返回。
	l.dirty = false
	if err := l.f.Close(); err != nil {
		l.dirty = true
		if reopenErr := l.reopenLocked(); reopenErr != nil {
			l.closed = true
			_ = os.Remove(compactPath)
			return fmt.Errorf("wal: 关闭旧日志失败（%w），且无法重新打开（%w）", err, reopenErr)
		}
		_ = os.Remove(compactPath)
		return fmt.Errorf("wal: 关闭旧日志失败: %w（旧日志已恢复，可稍后重试）", err)
	}
	l.f = nil

	if err := os.Rename(compactPath, l.path); err != nil {
		// 旧文件内容完整，尽力重新打开让写路径继续可用。
		if reopenErr := l.reopenLocked(); reopenErr != nil {
			l.closed = true
			_ = os.Remove(compactPath)
			return fmt.Errorf("wal: 压实改名失败（%w），旧日志也无法重新打开（%w）",
				err, reopenErr)
		}
		_ = os.Remove(compactPath)
		return fmt.Errorf("wal: 压实改名失败: %w（旧日志已恢复，可稍后重试）", err)
	}

	// 改名成功。重新打开并**完整扫描一遍**新文件。
	//
	// 扫描是多余的 I/O（内容是我们刚写的），但它能证明「写出来的确实
	// 是一份合法日志」。少了这一步，压实里任何一处记账错误都会变成
	// 「这次能跑，下次启动才发现读不回来」——那是最难查的一类问题。
	if err := l.reopenLocked(); err != nil {
		l.closed = true
		return fmt.Errorf("wal: 压实完成但重新打开失败: %w", err)
	}
	return nil
}

// appendDeltaLocked 把旧文件 [mark, size) 的原始字节接到 w 后面。
//
// 直接搬原始字节而不是解码再编码：那段本来就是合法记录，原样搬过去
// 保证新文件仍然是合法日志，也省掉一次编解码。
//
// 调用方必须持有 l.mu。
func (l *Log) appendDeltaLocked(w io.Writer, mark int64) error {
	if mark > l.size {
		return fmt.Errorf("wal: 基线偏移 %d 超过了日志长度 %d", mark, l.size)
	}
	n := l.size - mark
	if n == 0 {
		return nil
	}

	// 先刷一遍：同一进程内写读本来是一致的，但显式 Sync 让语义
	// 不依赖「页缓存对同一进程立即可见」这个假设。
	if err := l.syncLocked(); err != nil {
		return err
	}

	buf := make([]byte, n)
	if _, err := l.f.ReadAt(buf, mark); err != nil {
		return fmt.Errorf("wal: 读取增量记录失败: %w", err)
	}
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("wal: 写入增量记录失败: %w", err)
	}
	return nil
}

// reopenLocked 重新建立文件句柄并扫描。
//
// 两处调用：改名成功后指向新文件，改名失败后指回旧文件。
// 调用方必须持有 l.mu，且调用前应确保 l.f 已经关闭。
func (l *Log) reopenLocked() error {
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return fmt.Errorf("wal: 重新打开日志失败: %w", err)
	}

	l.f = f
	l.dirty = false
	l.size = 0

	if err := l.recover(); err != nil {
		_ = f.Close()
		l.f = nil
		return err
	}
	return nil
}

// Path 返回日志的文件路径。
func (l *Log) Path() string { return l.path }

// Version 返回这份日志的**实际**格式版本。
//
// 它可能低于 FormatVersion（读到的是旧日志），因此解码方必须按它来，
// 而不是按编译期的常量——否则旧日志会被按新布局解析，读出一堆乱码。
func (l *Log) Version() uint8 { return l.version }
