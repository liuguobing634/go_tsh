package corpus

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
)

// generateSeed 是固定种子：同样的 n 永远产生同样的语料，
// 这样压测结果才可复现、可对比。
const generateSeed = 20240101

// generateTopics 是语料里的「中频词」，会出现在一部分文档中。
var generateTopics = []string{
	"search", "index", "query", "token", "ranking", "score", "document",
	"field", "phrase", "boolean", "stemming", "analyzer", "shard", "replica",
	"latency", "throughput", "cache", "memory", "compression", "posting",
	"inverted", "relevance", "corpus", "filter", "segment", "snapshot",
	"vector", "embedding", "cluster", "partition", "offset", "cursor",
	"iterator", "batch", "stream", "pipeline", "ingest", "normalize",
	"tokenize", "lemmatize", "synonym", "stopword", "dictionary", "vocabulary",
	"prefix", "suffix", "wildcard", "fuzzy", "proximity", "boost",
	"weight", "threshold", "recall", "precision", "benchmark", "profile",
	"allocation", "concurrency", "goroutine", "channel",
}

// generateFillers 是填充词。它们大多会被停用词表过滤掉，
// 因此只占用位置、不产生 posting——真实文档就是这样。
var generateFillers = []string{
	"the", "a", "an", "and", "or", "of", "to", "in", "for", "with",
	"this", "that", "which", "while", "when", "then", "than", "from",
	"is", "are", "was", "were", "be", "been", "it", "its", "as", "at",
}

// Generate 合成 n 篇文档，用于本地压测与容量验证。
//
// # 为什么不是「每篇都从整个词表独立随机抽词」
//
// 那样做会得到一个退化的语料：词表只有几十个词，而每篇要抽上百个 token，
// 于是**每个词都出现在几乎每一篇文档里**，df 趋近于 N。
// 所有 posting 列表长度相同，测出来的分支与缓存行为是失真的——
// 而真实检索的开销差异，恰恰来自「命中集大小」这个维度。
//
// 这里的做法是：每篇文档只从词表里抽一个**子集**（3–8 个）并重复使用。
// 于是每个词只出现在约 picks/len(topics) 的文档里，df 落在合理区间；
// 再叠加一层只出现一两次的生僻词，形成长尾。
// 填充词会被停用词表过滤掉，只留下位置空隙。
func Generate(n int) []Document {
	if n <= 0 {
		return nil
	}

	rng := rand.New(rand.NewSource(generateSeed))
	docs := make([]Document, 0, n)

	for i := 0; i < n; i++ {
		// 本篇选中的主题词子集。
		picks := 3 + rng.Intn(6) // 3–8 个
		chosen := make([]string, 0, picks)
		for len(chosen) < picks {
			w := generateTopics[rng.Intn(len(generateTopics))]
			if !slices.Contains(chosen, w) {
				chosen = append(chosen, w)
			}
		}

		var title, body strings.Builder

		// 标题：从子集里取 2–4 个 + 编号。
		for j := 0; j < min(2+rng.Intn(3), len(chosen)); j++ {
			if j > 0 {
				title.WriteByte(' ')
			}
			title.WriteString(chosen[j])
		}
		fmt.Fprintf(&title, " %d", i)

		// 正文：120–200 个 token。
		bodyTokens := 120 + rng.Intn(81)
		for j := 0; j < bodyTokens; j++ {
			if j > 0 {
				body.WriteByte(' ')
			}

			switch roll := rng.Intn(100); {
			case roll < 20:
				body.WriteString(generateFillers[rng.Intn(len(generateFillers))])
			case roll < 30:
				// 生僻词：只在极少数文档里出现，形成长尾。
				fmt.Fprintf(&body, "rare%05d", rng.Intn(n))
			default:
				body.WriteString(chosen[rng.Intn(len(chosen))])
			}
		}

		docs = append(docs, Document{
			ID: fmt.Sprintf("gen-%07d", i),
			Fields: map[string]string{
				"title": title.String(),
				"body":  body.String(),
			},
		})
	}

	return docs
}
