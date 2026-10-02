package tablename

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	valid := []string{
		"a",
		"default",
		"products",
		"products2024",
		"product_v2",
		"product-v2",
		"a1",
		"0",
		"9lives",
		strings.Repeat("x", MaxLen),
	}

	for _, name := range valid {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) 应当通过，实际: %v", name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		why  string
	}{
		{"", "空串"},
		{"  ", "空白"},
		{"Products", "大写"},
		{"PRODUCTS", "全大写"},
		{"proDucts", "混合大小写"},
		{"_hidden", "下划线开头"},
		{"-dash", "连字符开头"},
		{"has space", "空格"},
		{"has.dot", "点"},
		{"has/slash", "斜杠"},
		{"has\\backslash", "反斜杠"},
		{"has:colon", "冒号"},
		{"has*star", "星号"},
		{"中文表名", "非 ASCII"},
		{"emoji😀", "emoji"},
		{strings.Repeat("x", MaxLen+1), "超长"},
	}

	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			err := Validate(tc.name)
			if err == nil {
				t.Fatalf("Validate(%q) 应当被拒绝", tc.name)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("错误应当包裹 ErrInvalid，实际: %v", err)
			}
		})
	}
}

// 路径穿越是这里最要防的东西：表名会被拼进文件路径。
func TestValidateRejectsPathTraversal(t *testing.T) {
	attacks := []string{
		"..",
		"../",
		"../etc/passwd",
		"../../etc/passwd",
		"..\\..\\windows\\system32",
		"/etc/passwd",
		"\\windows\\system32",
		"./relative",
		"a/../../b",
		"....//....//etc",
		"a\x00b", // 截断字符
		"a%2e%2e%2fb",
	}

	for _, name := range attacks {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err == nil {
				t.Fatalf("路径穿越 %q 必须被拒绝", name)
			}
		})
	}
}

// 大写被拒时必须给出可直接采用的小写形式，
// 否则用户只看到「不允许大写」还得自己拼。
func TestUppercaseErrorSuggestsLowercase(t *testing.T) {
	err := Validate("Products")
	if err == nil {
		t.Fatal("应当被拒绝")
	}
	if !strings.Contains(err.Error(), `"products"`) {
		t.Errorf("错误信息里应当给出建议的小写形式：%v", err)
	}
	t.Logf("%v", err)
}

func TestIsValid(t *testing.T) {
	if !IsValid("products") {
		t.Error("products 应当合法")
	}
	if IsValid("Products") {
		t.Error("Products 应当非法")
	}
}

// 默认表的名字本身必须合法，否则所有既有调用点都会在启动时炸。
func TestDefaultNameIsValid(t *testing.T) {
	if err := Validate(Default); err != nil {
		t.Fatalf("默认表名 %q 必须合法: %v", Default, err)
	}
}

// 校验本身不做归一化：它只回答「合不合法」。
// 悄悄改掉用户输入的表名会让人对不上号。
func TestValidateDoesNotMutate(t *testing.T) {
	name := "Products"
	_ = Validate(name)
	if name != "Products" {
		t.Error("Validate 不该修改传入的字符串")
	}
}
