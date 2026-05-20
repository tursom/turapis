package email

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain 在运行测试之前从项目根目录加载 .env 文件。
// .env 中的变量仅在环境变量中不存在时才会被设置。
func TestMain(m *testing.M) {
	loadDotEnv()
	os.Exit(m.Run())
}

// loadDotEnv 从项目根目录读取 .env 文件并设置所有
// 尚未定义的环境变量。
func loadDotEnv() {
	// 查找项目根目录：从 internal/email/ 向上查找模块根目录
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return // 到达文件系统根目录，放弃
		}
		dir = parent
	}

	path := filepath.Join(dir, ".env")
	f, err := os.Open(path)
	if err != nil {
		return // .env 是可选的
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// 如果有引号则移除
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}
