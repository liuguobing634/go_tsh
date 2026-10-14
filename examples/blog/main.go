// Command blog 演示「把 go_tsh 嵌进自己的 Go 程序」的最短路径。
//
// 直接跑：
//
//	go run ./examples/blog
//
// 它不启动 HTTP 服务、不落盘——引擎就跑在这个进程里。
// 想加 HTTP 接口见 README 的「作为库嵌入」一节。
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/liuguobing/go_tsh/pkg/tsh"
)

// post 是业务侧的类型。go_tsh 不要求你实现任何接口，
// 只需要在写入时把字段摊平即可。
//
// ⚠️ Category 是**单值**的。字段不支持多值——把 "go,search" 写进一个
// 关键字字段，它会被当成**一个**词条，`category:go` 匹配不上。
// 要多值就得拆成 `category_1` / `category_2` 这样的多个字段。
type post struct {
	Title    string
	Body     string
	Category string
	Likes    int
	Created  time.Time
}

func main() {
	// 1) 建引擎。字段类型必须声明——JSON/字符串里没有日期类型，
	//    靠猜格式会把版本号 "2024-01-01" 也当成日期。
	engine, err := tsh.NewWith(tsh.Options{
		Analyzer: tsh.AnalyzerChinese, // 中文走词典分词；纯英文可省
		Schema: map[string]tsh.FieldKind{
			"title":    tsh.FieldText,
			"body":     tsh.FieldText,
			"category": tsh.FieldKeyword, // 分类要精确匹配，不能分词
			"likes":    tsh.FieldNumber,
			"created":  tsh.FieldDate,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close()

	// 2) 灌数据。
	posts := []post{
		{"Go 的倒排索引怎么写", "从 posting list 讲到 BM25 的 k1 与 b", "go", 128, daysAgo(3)},
		{"中文分词的坑", "词典分词与子词扩展，以及为什么查询侧要用同一个分析器", "nlp", 256, daysAgo(1)},
		{"搜索为什么需要位置信息", "没有位置就只能做词袋匹配，短语查询无从谈起", "search", 64, daysAgo(7)},
		{"持久化的取舍", "只追加日志、重放、以及压实为什么是必须的", "go", 512, daysAgo(2)},
	}

	for i, p := range posts {
		if _, err := engine.Upsert(tsh.Document{
			ID:       fmt.Sprintf("post-%d", i+1),
			Fields:   map[string]string{"title": p.Title, "body": p.Body},
			Keywords: map[string]string{"category": p.Category},
			Numbers:  map[string]float64{"likes": float64(p.Likes)},
			Dates:    map[string]time.Time{"created": p.Created},
		}); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("已索引 %d 篇文章\n\n", len(posts))

	// 3) 按相关性搜，带高亮。
	show(engine, "搜索", "")

	// 4) 搜中文词 + 按点赞数排序。
	show(engine, "分词", "likes:desc")

	// 5) 精确匹配分类 + 按时间倒序。
	//
	//    关键字字段是**整值匹配**：`category:go` 只命中分类正好是 "go"
	//    的文章，不会命中 "golang"。这正是标签/枚举想要的语义。
	show(engine, "category:go", "created:desc")
}

// show 跑一次检索并把结果打印出来。
func show(engine *tsh.Engine, query, sortSpec string) {
	sortKeys, err := tsh.ParseSort(sortSpec)
	if err != nil {
		log.Fatal(err)
	}

	res, err := engine.Search(tsh.SearchRequest{
		Query:     query,
		Sort:      sortKeys,
		Limit:     5,
		Highlight: true,
	})
	if err != nil {
		// 排序字段写错、对文本字段做范围查询等等都会走到这里。
		// 这类错误**不会**被静默吞掉变成空结果——那样最难自查。
		log.Fatalf("检索 %q 失败: %v", query, err)
	}

	fmt.Printf("查询 %-12q 排序 %-14q 命中 %d 篇\n", query, sortSpec, res.Total)
	for _, h := range res.Hits {
		title := h.Fields["title"]
		if hl, ok := h.Highlights["title"]; ok {
			title = hl // 高亮片段里命中词被 <em> 标出
		}
		fmt.Printf("  %-28s 赞 %-4s %s\n",
			title, h.Fields["likes"], h.ID)
	}
	fmt.Println()
}

func daysAgo(n int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -n).Truncate(24 * time.Hour)
}
