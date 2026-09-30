package highlight

import (
	"strings"
	"testing"

	"github.com/liuguobing/go_tsh/internal/analyzer"
)

// keepAll 与检索测试用的是同一套配置，保证高亮与检索看到同样的词条。
func keepAll() analyzer.Analyzer {
	return analyzer.NewStandardWith(analyzer.StandardOptions{
		KeepStopwords: true,
		MinTokenLen:   1,
	})
}

func TestHighlightBasic(t *testing.T) {
	h := New(keepAll(), Options{})

	cases := []struct {
		name  string
		text  string
		terms []string
		want  string
	}{
		{
			name:  "单词命中",
			text:  "the quick brown fox",
			terms: []string{"quick"},
			want:  "the <em>quick</em> brown fox",
		},
		{
			name:  "多次命中",
			text:  "go go now go",
			terms: []string{"go"},
			want:  "<em>go</em> <em>go</em> now <em>go</em>",
		},
		{
			name:  "多词命中",
			text:  "the quick brown fox",
			terms: []string{"quick", "fox"},
			want:  "the <em>quick</em> brown <em>fox</em>",
		},
		{
			name:  "大小写不敏感（靠归一化）",
			text:  "Go and GO and go",
			terms: []string{"go"},
			want:  "<em>Go</em> and <em>GO</em> and <em>go</em>",
		},
		{
			// 按**词条**匹配而不是按子串匹配：
			// 只有独立的 the 被标注，there / theory 里的不算命中。
			name:  "按词条匹配而非子串",
			text:  "the there theory",
			terms: []string{"the"},
			want:  "<em>the</em> there theory",
		},
		{
			name:  "中文逐字标注",
			text:  "全文搜索服务",
			terms: []string{"搜", "索"},
			want:  "全文<em>搜</em><em>索</em>服务",
		},
		{
			name:  "全角折叠后仍按原文区间标注",
			text:  "ＡＢ normal",
			terms: []string{"ab"},
			want:  "<em>ＡＢ</em> normal",
		},
		{
			// html.EscapeString 连撇号一起转义成 &#39;。
			// 这是刻意的保守做法：调用方不必关心输出会被放进文本节点
			// 还是属性值里，渲染结果都一样。
			name:  "词内撇号（撇号会被 HTML 转义）",
			text:  "don't stop",
			terms: []string{"don't"},
			want:  "<em>don&#39;t</em> stop",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.Highlight(tc.text, tc.terms); got != tc.want {
				t.Fatalf("Highlight(%q, %v)\n got: %s\nwant: %s", tc.text, tc.terms, got, tc.want)
			}
		})
	}
}

func TestHighlightNoMatch(t *testing.T) {
	h := New(keepAll(), Options{})

	for _, tc := range []struct {
		name  string
		text  string
		terms []string
	}{
		{"词条不存在", "hello world", []string{"missing"}},
		{"空词条表", "hello world", nil},
		{"空文本", "", []string{"hello"}},
		{"词条表里全是空串", "hello world", []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.Highlight(tc.text, tc.terms); got != "" {
				t.Errorf("无命中时应返回空串，实际 %q", got)
			}
		})
	}
}

// 原文里的 HTML 必须被转义，插入的标记是输出里仅有的原始 HTML。
// 否则检索结果页会有 XSS。
func TestHighlightEscapesHTML(t *testing.T) {
	h := New(keepAll(), Options{})

	cases := []struct {
		name  string
		text  string
		terms []string
		want  string
	}{
		{
			name:  "尖括号被转义",
			text:  "say <script>alert(1)</script> now",
			terms: []string{"alert"},
			want:  "say &lt;script&gt;<em>alert</em>(1)&lt;/script&gt; now",
		},
		{
			name:  "与号和引号被转义",
			text:  `a & b "quoted" tag`,
			terms: []string{"tag"},
			want:  `a &amp; b &#34;quoted&#34; <em>tag</em>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.Highlight(tc.text, tc.terms)
			if got != tc.want {
				t.Fatalf("Highlight(%q)\n got: %s\nwant: %s", tc.text, got, tc.want)
			}
			// 除自己插入的标记外，不应再有裸的尖括号。
			stripped := strings.ReplaceAll(got, DefaultPreTag, "")
			stripped = strings.ReplaceAll(stripped, DefaultPostTag, "")
			if strings.ContainsAny(stripped, "<>") {
				t.Errorf("转义不彻底，仍有裸尖括号: %s", got)
			}
		})
	}
}

func TestHighlightCustomTags(t *testing.T) {
	h := New(keepAll(), Options{PreTag: "[", PostTag: "]"})

	if got, want := h.Highlight("a quick fox", []string{"quick"}), "a [quick] fox"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHighlightTruncation(t *testing.T) {
	h := New(keepAll(), Options{MaxLen: 40})

	text := strings.Repeat("padding padding padding ", 10) + "target" + strings.Repeat(" padding", 50)

	got := h.Highlight(text, []string{"target"})
	if got == "" {
		t.Fatal("应当命中 target")
	}
	if !strings.Contains(got, "<em>target</em>") {
		t.Fatalf("截断后仍须包含命中词条: %s", got)
	}
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("首尾都被截断时应加省略号: %s", got)
	}
	// 标记之外的可见内容不应超过窗口太多（留出标记与省略号的开销）。
	if len(got) > 40+len(DefaultPreTag)+len(DefaultPostTag)+16 {
		t.Errorf("输出过长（%d 字节）: %s", len(got), got)
	}
}

// 截断时窗口边界必须落在 rune 边界上，不能把汉字切成半个。
func TestHighlightTruncationKeepsRunesIntact(t *testing.T) {
	h := New(keepAll(), Options{MaxLen: 18})

	text := strings.Repeat("全文搜索服务", 20)

	got := h.Highlight(text, []string{"搜"})
	if got == "" {
		t.Fatal("应当命中")
	}

	// 去掉省略号与标记后，剩下的应是合法 UTF-8 且只含原文本出现过的字符。
	clean := strings.NewReplacer("…", "", DefaultPreTag, "", DefaultPostTag, "").Replace(got)
	if strings.ContainsRune(clean, '\uFFFD') {
		t.Fatalf("出现替换字符，说明切断了多字节字符: %q", got)
	}
	for _, r := range clean {
		if !strings.ContainsRune("全文搜索服务", r) {
			t.Fatalf("出现原文本之外的字符 %q: %s", r, got)
		}
	}
}

func TestHighlightNoTruncationWhenShort(t *testing.T) {
	h := New(keepAll(), Options{MaxLen: 1000})

	got := h.Highlight("a quick fox", []string{"quick"})
	if strings.Contains(got, "…") {
		t.Errorf("文本短于 MaxLen 时不应截断: %s", got)
	}
}

func BenchmarkHighlight(b *testing.B) {
	h := New(analyzer.NewStandard(), Options{MaxLen: 160})
	text := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running ", 4)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = h.Highlight(text, []string{"quick", "running"})
	}
}
