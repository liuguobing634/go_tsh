// Package corpus 提供本地演示与压测用的文档来源：从目录导入，或合成。
//
// 它不属于服务运行时的一部分，只在启动时被命令行开关调用一次。
package corpus

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Document 是导入或合成出来的一篇文档。
type Document struct {
	// ID 是文档标识：导入时取文件名（不含扩展名），合成时是 gen-<序号>。
	ID string

	// Fields 是字段内容。导入时含 title 与 body。
	Fields map[string]string
}

// importExtensions 是导入时认可的文本扩展名。
var importExtensions = []string{".txt", ".md", ".markdown", ".text", ".log"}

// maxImportBytes 是单文件导入上限。超过就跳过并记入错误，
// 而不是让索引的 token 上限去报一个语焉不详的错。
const maxImportBytes = 4 << 20 // 4 MiB

// ErrNoDocuments 表示目录里没有找到任何可导入的文件。
var ErrNoDocuments = errors.New("corpus: 目录中没有可导入的文本文件")

// Load 读取 dir 下的文本文件，转成文档。
//
// 规则：
//   - 只认 importExtensions 里的扩展名；
//   - 文档 ID 取文件名去掉扩展名，因此同一目录内的文件名必须唯一；
//   - title 字段取文件名（不含扩展名），body 字段是文件全部内容；
//   - 跳过隐藏文件与隐藏目录；子目录会递归。
//
// 返回的切片按 ID 排序，保证同样的目录每次导入的结果一致。
func Load(dir string) ([]Document, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("corpus: 无法访问目录 %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("corpus: %s 不是目录", dir)
	}

	var (
		docs []Document
		errs []error
	)

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			return nil
		}

		name := d.Name()
		// 跳过隐藏项（.git、.DS_Store 之类）。
		if name != "." && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		if !hasImportExtension(name) {
			return nil
		}

		content, err := readCapped(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			return nil
		}

		base := strings.TrimSuffix(name, filepath.Ext(name))
		docs = append(docs, Document{
			ID: base,
			Fields: map[string]string{
				"title": base,
				"body":  content,
			},
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("corpus: 遍历 %s 失败: %w", dir, walkErr)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoDocuments, dir)
	}

	slices.SortFunc(docs, func(a, b Document) int {
		return strings.Compare(a.ID, b.ID)
	})
	return docs, nil
}

func hasImportExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return slices.Contains(importExtensions, ext)
}

func readCapped(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxImportBytes {
		return "", fmt.Errorf("文件 %d 字节，超过 %d 上限", info.Size(), maxImportBytes)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
