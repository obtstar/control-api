// pipeline.yaml 热加载测试（FINDING-008）：文件 mtime 变化后下一次调用反映新配置；
// 非法配置拒载且旧配置继续可用（不得因坏配置打爆运行中的服务）。
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePipeline 写入 pipeline.yaml 并把 mtime 拨到 base 之后，保证与上次加载的 mtime 可区分
func writePipeline(t *testing.T, path, stages string, mtime time.Time) {
	t.Helper()
	content := "pipeline:\n  stages:" + stages
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

const hotStagesV1 = `
    - id: coding
      approval: required
    - id: deliver
      approval: auto
`

// 改文件内容 → 下一次调用反映新阶段配置
func TestHotReloadReflectsNewStages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	base := time.Now()
	writePipeline(t, path, hotStagesV1, base)

	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.First(); got != "coding" {
		t.Fatalf("First = %q, want coding", got)
	}
	next, err := p.Next("coding")
	if err != nil || next != "deliver" {
		t.Fatalf("Next(coding) = %q, %v; want deliver", next, err)
	}

	// 插入 testing 阶段（mtime 前进，模拟编辑）
	writePipeline(t, path, `
    - id: coding
      approval: required
    - id: testing
      approval: required
    - id: deliver
      approval: auto
`, base.Add(time.Minute))

	next, err = p.Next("coding")
	if err != nil || next != "testing" {
		t.Fatalf("热加载后 Next(coding) = %q, %v; want testing", next, err)
	}
	if !p.NeedsApproval("testing") {
		t.Fatal("热加载后 testing 应为 required")
	}
	if got := p.RejectTarget("testing"); got != "testing" {
		t.Fatalf("RejectTarget = %q, want testing（默认重做本阶段）", got)
	}
}

// 改出非法配置（merge=auto）→ 拒载且旧配置继续可用
func TestHotReloadRejectsInvalidKeepsOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	base := time.Now()
	writePipeline(t, path, hotStagesV1, base)

	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	writePipeline(t, path, `
    - id: coding
      approval: required
    - id: merge
      approval: auto
    - id: deliver
      approval: auto
`, base.Add(time.Minute))

	// 非法配置不得生效：阶段集仍为旧配置，且服务可继续应答
	if got := p.First(); got != "coding" {
		t.Fatalf("坏配置不应生效: First = %q, want coding", got)
	}
	if next, _ := p.Next("coding"); next != "deliver" {
		t.Fatalf("坏配置不应生效: Next(coding) = %q, want deliver", next)
	}
	if _, err := p.Next("merge"); err == nil {
		t.Fatal("旧配置无 merge 阶段，应报未知阶段（说明坏配置未混入）")
	}

	// 修回合法配置 → 恢复热加载
	writePipeline(t, path, `
    - id: coding
      approval: required
    - id: merge
      approval: team_mr_review
    - id: deliver
      approval: auto
`, base.Add(2*time.Minute))
	if !p.IsTeamReview("merge") {
		t.Fatal("修回合法配置后 merge 应为 team_mr_review")
	}
}

// 并发读 + 后台改文件：不得 panic / 数据竞争（配合 -race 验证）
func TestHotReloadConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yaml")
	base := time.Now()
	writePipeline(t, path, hotStagesV1, base)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { // 后台反复改文件触发热加载；不用 t（非测试 goroutine）
		for i := 1; i <= 5; i++ {
			mt := base.Add(time.Duration(i) * time.Minute)
			if err := os.WriteFile(path, []byte("pipeline:\n  stages:"+hotStagesV1), 0o644); err != nil {
				done <- err
				return
			}
			if err := os.Chtimes(path, mt, mt); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
			_ = p.NeedsApproval("coding")
			_, _ = p.Next("coding")
			_ = p.Model("coding")
		}
	}
}
