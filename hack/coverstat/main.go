// Command coverstat 统计 Go 覆盖率文件的分包覆盖率，供覆盖率门禁（≥85%）使用。
// 支持合并多个覆盖率文件（同一代码块取最大命中次数），例如各包独立测试后再汇总。
// 用法：go run ./hack/coverstat -profile "logic.out,store.out,controller.out,model.out" -min 85
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

type block struct {
	filePath string
	stmts    int
	count    int
}

type stat struct {
	stmts   int
	covered int
}

func main() {
	profiles := flag.String("profile", "coverage.out", "覆盖率文件，逗号分隔可合并多个")
	min := flag.Float64("min", 85, "各包与整体的最低覆盖率（百分比）")
	flag.Parse()

	blocks := map[string]*block{}
	for _, profile := range strings.Split(*profiles, ",") {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		if err := load(profile, blocks); err != nil {
			fmt.Fprintf(os.Stderr, "读取覆盖率文件 %s 失败: %v\n", profile, err)
			os.Exit(2)
		}
	}
	if len(blocks) == 0 {
		fmt.Fprintln(os.Stderr, "覆盖率数据为空")
		os.Exit(2)
	}

	byPackage := map[string]*stat{}
	for _, b := range blocks {
		pkg := path.Dir(b.filePath)
		s, ok := byPackage[pkg]
		if !ok {
			s = &stat{}
			byPackage[pkg] = s
		}
		s.stmts += b.stmts
		if b.count > 0 {
			s.covered += b.stmts
		}
	}

	packages := make([]string, 0, len(byPackage))
	for pkg := range byPackage {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	total := stat{}
	failed := false
	fmt.Printf("%-52s %10s %10s\n", "PACKAGE", "STMTS", "COVERAGE")
	for _, pkg := range packages {
		s := byPackage[pkg]
		total.stmts += s.stmts
		total.covered += s.covered
		rate := percent(s.covered, s.stmts)
		mark := ""
		if rate < *min {
			mark = "  <-- 低于门禁"
			failed = true
		}
		fmt.Printf("%-52s %10d %9.1f%%%s\n", pkg, s.stmts, rate, mark)
	}
	totalRate := percent(total.covered, total.stmts)
	fmt.Printf("%-52s %10d %9.1f%%\n", "TOTAL", total.stmts, totalRate)
	if totalRate < *min {
		failed = true
	}
	if failed {
		fmt.Fprintf(os.Stderr, "覆盖率门禁未通过：要求各包与整体均 >= %.1f%%\n", *min)
		os.Exit(1)
	}
	fmt.Printf("覆盖率门禁通过：各包与整体均 >= %.1f%%\n", *min)
}

func load(profile string, blocks map[string]*block) error {
	file, err := os.Open(profile)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		filePath := line[:colon]
		rangeAndCount := line[colon+1:]
		fields := strings.Fields(rangeAndCount)
		if len(fields) < 3 {
			continue
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		key := filePath + ":" + fields[0]
		if existing, ok := blocks[key]; ok {
			if count > existing.count {
				existing.count = count
			}
			continue
		}
		blocks[key] = &block{filePath: filePath, stmts: stmts, count: count}
	}
	return scanner.Err()
}

func percent(covered, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(covered) * 100 / float64(total)
}
