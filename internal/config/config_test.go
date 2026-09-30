package config

import (
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
