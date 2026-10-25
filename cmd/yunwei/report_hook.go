package main

import (
	"context"

	"yunwei/internal/config"
	"yunwei/internal/inspect"
	"yunwei/internal/report"
)

// reportFileHook renders and writes <out_dir>/<runID>.md and .html for every
// completed run, whatever the trigger (schedule, manual API, CLI). The output
// directory is the -out flag when given, else report.out_dir from the config.
func reportFileHook(b *config.Bundle, outDirOverride string) func(context.Context, *inspect.Outcome) (*inspect.ReportFiles, error) {
	dir := outDirOverride
	if dir == "" {
		dir = b.App.Report.OutDir
	}
	if dir == "" {
		dir = "reports"
	}
	return func(_ context.Context, out *inspect.Outcome) (*inspect.ReportFiles, error) {
		files, err := report.Write(dir, report.Build(report.FromOutcome(out, b.Inspection.Items)))
		if err != nil {
			return nil, err
		}
		return &inspect.ReportFiles{Markdown: files.Markdown, HTML: files.HTML}, nil
	}
}
