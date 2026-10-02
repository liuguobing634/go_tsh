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
		header := make([]byte, headerSize)
		copy(header, Magic)
		header[4] = FormatVersion
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

	buf := l.buf[:0]
	buf = append(buf, byte(kind))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	// 校验和覆盖 kind + length + payload：长度字段被写坏也能被发现。
	buf = binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf))
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

// Path 返回日志的文件路径。
func (l *Log) Path() string { return l.path }

// Version 返回这份日志的**实际**格式版本。
//
// 它可能低于 FormatVersion（读到的是旧日志），因此解码方必须按它来，
// 而不是按编译期的常量——否则旧日志会被按新布局解析，读出一堆乱码。
func (l *Log) Version() uint8 { return l.version }
