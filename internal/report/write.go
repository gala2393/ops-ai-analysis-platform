package report

import (
	"fmt"
	"os"
	"path/filepath"
)

// Files are the paths of a written report.
type Files struct {
	Markdown string
	HTML     string
}

// Write renders the report and writes <dir>/<runID>.md and <dir>/<runID>.html.
func Write(dir string, r *Report) (Files, error) {
	var f Files
	html, err := r.HTML()
	if err != nil {
		return f, fmt.Errorf("渲染 HTML 报告失败: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return f, fmt.Errorf("无法创建报告目录 %s: %w", dir, err)
	}
	f.Markdown = filepath.Join(dir, fmt.Sprintf("%d.md", r.RunID))
	f.HTML = filepath.Join(dir, fmt.Sprintf("%d.html", r.RunID))
	if err := os.WriteFile(f.Markdown, []byte(r.Markdown()), 0o644); err != nil {
		return f, fmt.Errorf("写入 Markdown 报告失败: %w", err)
	}
	if err := os.WriteFile(f.HTML, []byte(html), 0o644); err != nil {
		return f, fmt.Errorf("写入 HTML 报告失败: %w", err)
	}
	return f, nil
}
