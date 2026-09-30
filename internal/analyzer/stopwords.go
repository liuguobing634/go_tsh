package analyzer

import (
	_ "embed"
	"strings"
)

// stopwords.txt 以 '#' 起始的行是注释，其余按空白切分。
//
//go:embed stopwords.txt
var defaultStopwordsRaw string

// parseStopwords 把停用词表文本解析成词条切片。
//
// 支持三种写法：每行一个词、一行多个词、以及 '#' 起始的注释行。
func parseStopwords(raw string) []string {
	var words []string
	for _, line := range strings.Split(raw, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		words = append(words, strings.Fields(line)...)
	}
	return words
}
