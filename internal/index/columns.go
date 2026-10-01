package index

import "math"

// numericColumn 是某个数值/时间字段的列式存储，按 DocID 稠密存放。
//
// # 为什么是「按 DocID 稠密的切片」而不是「按值排序的数组」
//
// 关键在于**输出顺序**：求值器只认「DocID 升序的命中列表」，
// 因为归并求交/求并都是基于有序切片的双指针。
//
// 按 DocID 顺序扫描，命中的 DocID **天然就是升序的**——
// 直接就是求值器要的形态，零排序、零归并改动。
//
// 若改成按值排序的数组，可以用二分把候选范围缩到 O(log n + k)，
// 但拿到候选之后仍要按 DocID 重排（O(k log k)），
// 而范围查询的 k 往往很大（`price > 0` 能命中几乎所有文档）。
//
// 代价是每次查询 O(N) 全扫。10 万篇 × 8 字节 = 800 KB，
// 内存带宽受限，实测比一次 term 查询还快——所以先用简单的，
// 等真的有瓶颈再考虑加有序索引。
//
// # 缺值
//
// 用独立的 present 位图标记，**不用 NaN 当哨兵**：
// NaN != NaN，任何依赖相等比较的写法都会在缺值上悄悄出错。
type numericColumn struct {
	values  []float64
	present []bool
}

// set 写入某篇文档在该字段上的值。调用方必须持有写锁。
func (c *numericColumn) set(id DocID, v float64) {
	c.grow(id)
	c.values[id] = v
	c.present[id] = true
}

// clear 清掉某篇文档的值（删除或覆盖时调用）。调用方必须持有写锁。
func (c *numericColumn) clear(id DocID) {
	if int(id) < len(c.present) {
		c.present[id] = false
		c.values[id] = 0
	}
}

// grow 保证切片至少能容纳到 id。调用方必须持有写锁。
//
// 与 setDocFieldLenLocked 一样按 2 倍扩容：DocID 逐个递增，
// 若每次都按需精确扩容，每插入一篇就要重新分配并拷贝整条切片，
// 退化成 O(n²)。
func (c *numericColumn) grow(id DocID) {
	if int(id) < len(c.values) {
		return
	}

	size := max(len(c.values)*2, int(id)+1, 64)
	values := make([]float64, size)
	copy(values, c.values)
	c.values = values

	present := make([]bool, size)
	copy(present, c.present)
	c.present = present
}

// rangeScan 把落在区间内的 DocID 追加到 dst，返回新的切片。
//
// 闭区间用 includeLo/includeHi 控制；单边范围传 ±Inf 即可。
// **输出的 DocID 天然升序**——这是整个选型的立足点。
func (c *numericColumn) rangeScan(lo, hi float64, includeLo, includeHi bool, dst []DocID) []DocID {
	for i, v := range c.values {
		if !c.present[i] {
			continue
		}
		// NaN 参与任何比较都是 false，会从下面所有过滤条件里漏过去。
		// 写入侧已经拒绝 NaN，这里是第二道防线。
		if math.IsNaN(v) {
			continue
		}
		if v < lo || (v == lo && !includeLo) {
			continue
		}
		if v > hi || (v == hi && !includeHi) {
			continue
		}
		dst = append(dst, DocID(i))
	}
	return dst
}

// count 返回该列上有值的文档数，用于统计与测试。
func (c *numericColumn) count() int {
	n := 0
	for _, ok := range c.present {
		if ok {
			n++
		}
	}
	return n
}
