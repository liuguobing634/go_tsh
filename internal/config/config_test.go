package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noEnv 是「环境里什么都没有」的 lookup 桩。
func noEnv(string) string { return "" }

// mapEnv 把 map 适配成 lookup 函数。
func mapEnv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("默认配置应当合法，却返回错误: %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, noEnv)
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	def := Default()
	if cfg != def {
		t.Fatalf("空输入应得到默认配置\n got: %+v\nwant: %+v", cfg, def)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	cfg, err := Load(nil, mapEnv(map[string]string{
		"TSH_ADDR":            ":9999",
		"TSH_LOG_LEVEL":       "debug",
		"TSH_MAX_QUERY_TERMS": "8",
		"TSH_QUERY_TIMEOUT":   "1500ms",
	}))
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Addr", cfg.Addr, ":9999"},
		{"LogLevel", cfg.LogLevel, "debug"},
		{"MaxQueryTerms", cfg.MaxQueryTerms, 8},
		{"QueryTimeout", cfg.QueryTimeout, 1500 * time.Millisecond},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if cfg.MaxDocTokens != Default().MaxDocTokens {
		t.Errorf("未被环境变量覆盖的项应保持默认值，MaxDocTokens = %d", cfg.MaxDocTokens)
	}
}

func TestLoadFlagBeatsEnv(t *testing.T) {
	cfg, err := Load([]string{"-addr", ":7777"}, mapEnv(map[string]string{"TSH_ADDR": ":9999"}))
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if cfg.Addr != ":7777" {
		t.Fatalf("命令行显式指定的值应优先于环境变量，Addr = %q, want %q", cfg.Addr, ":7777")
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"非法日志级别", nil, map[string]string{"TSH_LOG_LEVEL": "verbose"}},
		{"环境变量非数字", nil, map[string]string{"TSH_MAX_BODY_BYTES": "abc"}},
		{"环境变量非法时长", nil, map[string]string{"TSH_QUERY_TIMEOUT": "3 秒"}},
		{"上限为负", []string{"-max-query-terms", "-1"}, nil},
		{"上限为零", []string{"-max-doc-fields", "0"}, nil},
		{"合成文档数为负", []string{"-generate", "-1"}, nil},
		{"地址为空白", []string{"-addr", "   "}, nil},
		{"超时为负", []string{"-read-timeout", "-1s"}, nil},
		{"未知参数", []string{"-nope"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, mapEnv(tc.env)); err == nil {
				t.Fatalf("期望返回错误，实际通过了校验")
			}
		})
	}
}

func TestEnvKey(t *testing.T) {
	cases := []struct{ flagName, want string }{
		{"addr", "TSH_ADDR"},
		{"log-level", "TSH_LOG_LEVEL"},
		{"max-doc-tokens", "TSH_MAX_DOC_TOKENS"},
		{"query-timeout", "TSH_QUERY_TIMEOUT"},
	}
	for _, c := range cases {
		if got := EnvKey(c.flagName); got != c.want {
			t.Errorf("EnvKey(%q) = %q, want %q", c.flagName, got, c.want)
		}
	}
}

func TestAnalyzerConfig(t *testing.T) {
	t.Run("默认是 standard", func(t *testing.T) {
		cfg, err := Load(nil, mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Analyzer != AnalyzerStandard {
			t.Errorf("默认 analyzer = %q, want %q", cfg.Analyzer, AnalyzerStandard)
		}
		if cfg.DictPath != "" {
			t.Errorf("默认 dict 应为空，实际 %q", cfg.DictPath)
		}
		if cfg.NoSubWords {
			t.Error("默认不应关闭子词")
		}
	})

	t.Run("可选 chinese", func(t *testing.T) {
		cfg, err := Load([]string{"-analyzer", "chinese"}, mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Analyzer != AnalyzerChinese {
			t.Errorf("analyzer = %q, want %q", cfg.Analyzer, AnalyzerChinese)
		}
	})

	t.Run("环境变量可设置", func(t *testing.T) {
		cfg, err := Load(nil, mapEnv(map[string]string{"TSH_ANALYZER": "chinese"}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Analyzer != AnalyzerChinese {
			t.Errorf("analyzer = %q, want %q", cfg.Analyzer, AnalyzerChinese)
		}
	})

	// 非法值与「设了中文专属参数却没开中文」都必须报错。
	// 静默忽略是最糟的处理：用户以为配置生效了，实际没有。
	t.Run("非法值报错", func(t *testing.T) {
		if _, err := Load([]string{"-analyzer", "martian"}, mapEnv(nil)); err == nil {
			t.Error("未知 analyzer 应当报错")
		}
	})

	t.Run("dict 需要 chinese", func(t *testing.T) {
		if _, err := Load([]string{"-dict", "words.txt"}, mapEnv(nil)); err == nil {
			t.Error("只给 dict 不给 analyzer 应当报错")
		}
		if _, err := Load([]string{"-analyzer", "chinese", "-dict", "words.txt"}, mapEnv(nil)); err != nil {
			t.Errorf("analyzer=chinese 时 dict 应当被接受: %v", err)
		}
	})

	t.Run("no-sub-words 需要 chinese", func(t *testing.T) {
		if _, err := Load([]string{"-no-sub-words"}, mapEnv(nil)); err == nil {
			t.Error("只给 no-sub-words 不给 analyzer 应当报错")
		}
		if _, err := Load([]string{"-analyzer", "chinese", "-no-sub-words"}, mapEnv(nil)); err != nil {
			t.Errorf("analyzer=chinese 时 no-sub-words 应当被接受: %v", err)
		}
	})
}

func TestPersistenceConfig(t *testing.T) {
	t.Run("默认不持久化", func(t *testing.T) {
		cfg, err := Load(nil, mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DataDir != "" {
			t.Errorf("默认 data-dir 应为空（纯内存），实际 %q", cfg.DataDir)
		}
		if cfg.SyncInterval != 100*time.Millisecond {
			t.Errorf("默认 sync-interval = %s, want 100ms", cfg.SyncInterval)
		}
	})

	t.Run("命令行与环境变量", func(t *testing.T) {
		cfg, err := Load([]string{"-data-dir", "./data", "-sync-interval", "5ms"}, mapEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DataDir != "./data" {
			t.Errorf("data-dir = %q", cfg.DataDir)
		}
		if cfg.SyncInterval != 5*time.Millisecond {
			t.Errorf("sync-interval = %s", cfg.SyncInterval)
		}

		cfg, err = Load(nil, mapEnv(map[string]string{
			"TSH_DATA_DIR":      "/var/lib/tsh",
			"TSH_SYNC_INTERVAL": "1s",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DataDir != "/var/lib/tsh" {
			t.Errorf("从环境变量读到的 data-dir = %q", cfg.DataDir)
		}
		if cfg.SyncInterval != time.Second {
			t.Errorf("从环境变量读到的 sync-interval = %s", cfg.SyncInterval)
		}
	})

	// 0 不能表示「不后台刷盘」：它是 tsh.Options 的零值，
	// 那里把它解释为「用默认间隔」。同一个值两处含义不同迟早出事。
	t.Run("sync-interval 必须为正", func(t *testing.T) {
		for _, v := range []string{"0s", "-1s"} {
			if _, err := Load([]string{"-sync-interval", v}, mapEnv(nil)); err == nil {
				t.Errorf("sync-interval=%s 应当报错", v)
			}
		}
	})
}

func TestParseMapping(t *testing.T) {
	t.Run("空表示不声明", func(t *testing.T) {
		got, err := ParseMapping("   ")
		if err != nil || got != nil {
			t.Errorf("空输入应当返回 nil, nil，实际 %v, %v", got, err)
		}
	})

	t.Run("正常解析", func(t *testing.T) {
		got, err := ParseMapping(" created : DATE , sku:keyword ,price:number ")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"created": "date", "sku": "keyword", "price": "number"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("非法输入", func(t *testing.T) {
		for _, bad := range []string{
			"created",              // 缺少冒号
			"created:",             // 缺少类型
			":date",                // 缺少字段名
			"created:martian",      // 未知类型
			"a:date,b:date,c:date", // 合法
		} {
			_, err := ParseMapping(bad)
			if bad == "a:date,b:date,c:date" {
				if err != nil {
					t.Errorf("%q 应当合法: %v", bad, err)
				}
				continue
			}
			if err == nil {
				t.Errorf("%q 应当报错", bad)
			}
		}
	})

	t.Run("同一字段声明两次要报错", func(t *testing.T) {
		if _, err := ParseMapping("a:date,a:keyword"); err == nil {
			t.Error("重复声明应当报错")
		}
	})
}

func TestMappingConfig(t *testing.T) {
	cfg, err := Load([]string{"-mapping", "created:date,sku:keyword"}, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mapping != "created:date,sku:keyword" {
		t.Errorf("mapping = %q", cfg.Mapping)
	}

	// 非法值必须在启动时就报错，而不是等到第一次写入
	if _, err := Load([]string{"-mapping", "created:martian"}, mapEnv(nil)); err == nil {
		t.Error("非法的 mapping 应当让启动失败")
	}

	// 环境变量同样可用
	cfg, err = Load(nil, mapEnv(map[string]string{"TSH_MAPPING": "created:date"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mapping != "created:date" {
		t.Errorf("从环境变量读到的 mapping = %q", cfg.Mapping)
	}
}

func TestParseMappingFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("空路径", func(t *testing.T) {
		got, err := ParseMappingFile("")
		if err != nil || got != nil {
			t.Errorf("空路径应当返回 nil, nil，实际 %v, %v", got, err)
		}
	})

	t.Run("正常解析", func(t *testing.T) {
		p := write("ok.json", `{
			"products": {"price": "number", "created": "date"},
			"articles": {"tag": "keyword", "title": "TEXT"}
		}`)
		got, err := ParseMappingFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("解析出 %d 张表，want 2（%v）", len(got), got)
		}
		if got["products"]["price"] != "number" {
			t.Errorf("products.price = %q", got["products"]["price"])
		}
		// 类型名大小写不敏感
		if got["articles"]["title"] != "text" {
			t.Errorf("类型名应当归一成小写，实际 %q", got["articles"]["title"])
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		if _, err := ParseMappingFile(filepath.Join(dir, "nope.json")); err == nil {
			t.Error("文件不存在应当报错")
		}
	})

	t.Run("非法 JSON", func(t *testing.T) {
		if _, err := ParseMappingFile(write("bad.json", `{oops`)); err == nil {
			t.Error("非法 JSON 应当报错")
		}
	})

	t.Run("空对象", func(t *testing.T) {
		if _, err := ParseMappingFile(write("empty.json", `{}`)); err == nil {
			t.Error("没有任何表应当报错")
		}
	})

	t.Run("表名为空", func(t *testing.T) {
		if _, err := ParseMappingFile(write("noname.json", `{"": {"a":"text"}}`)); err == nil {
			t.Error("空表名应当报错")
		}
	})

	t.Run("非法表名", func(t *testing.T) {
		if _, err := ParseMappingFile(write("badname.json", `{"Products": {"a":"text"}}`)); err == nil {
			t.Error("大写表名应当报错")
		}
	})

	t.Run("未知类型", func(t *testing.T) {
		if _, err := ParseMappingFile(write("badkind.json", `{"t": {"a":"martian"}}`)); err == nil {
			t.Error("未知类型应当报错")
		}
	})

	t.Run("表没有字段", func(t *testing.T) {
		if _, err := ParseMappingFile(write("nofields.json", `{"t": {}}`)); err == nil {
			t.Error("空字段表应当报错")
		}
	})
}

func TestImportTableConfig(t *testing.T) {
	cfg, err := Load([]string{"-import-table", "products"}, mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ImportTable != "products" {
		t.Errorf("import-table = %q", cfg.ImportTable)
	}

	// 非法表名必须在启动时就报错
	if _, err := Load([]string{"-import-table", "Products"}, mapEnv(nil)); err == nil {
		t.Error("非法的 import-table 应当让启动失败")
	}
}
