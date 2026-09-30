// Package scoring 实现 BM25 相关性打分与 Top-K 选择。
//
// 本包只做**纯计算**：文档数、文档频率、平均长度等索引统计量
// 全部由调用方传入，因此它不依赖 index 包，可以独立测试与复用。
package scoring

import "math"

// BM25 是 Okapi BM25 的参数组。
type BM25 struct {
	// K1 控制词频饱和速度：越大，词频增长带来的增益衰减越慢。
	K1 float64

	// B 控制长度归一化强度：0 表示完全不归一化，1 表示完全归一化。
	B float64
}

// DefaultBM25 返回业界常用的一组参数。
func DefaultBM25() BM25 { return BM25{K1: 1.2, B: 0.75} }

// IDF 计算逆文档频率。
//
// 采用 Lucene 的平滑写法：
//
//	ln(1 + (N - df + 0.5) / (df + 0.5))
//
// 相比经典的 ln((N-df+0.5)/(df+0.5))，它**恒为正**。
// 经典写法在 df > N/2（即词条出现在超过一半文档中）时会给出负分，
// 把命中该词的文档压到完全没命中的文档之下——这是很反直觉的排序 bug。
func (bm BM25) IDF(docFreq, docCount uint32) float64 {
	if docCount == 0 {
		return 0
	}
	return math.Log(1 + (float64(docCount)-float64(docFreq)+0.5)/(float64(docFreq)+0.5))
}

// Score 计算「单字段、单词条」对一个文档的 BM25 贡献。
//
// 调用方先用 IDF 求出 idf，再对每个命中文档调用本方法。
// 总分是各字段、各词条贡献之和——注意 IDF 必须按字段分别算。
func (bm BM25) Score(idf float64, tf uint32, docLen, avgDocLen float64) float64 {
	// 空字段或空索引没有长度基准，无从归一化，只能记 0 分。
	if tf == 0 || avgDocLen <= 0 {
		return 0
	}

	norm := 1 - bm.B + bm.B*(docLen/avgDocLen)
	return idf * (float64(tf) * (bm.K1 + 1)) / (float64(tf) + bm.K1*norm)
}
