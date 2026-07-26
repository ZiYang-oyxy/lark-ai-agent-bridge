package update

import (
	"strings"
	"testing"
)

func TestHideEmptyReleaseNoteSections(t *testing.T) {
	notes := "# v1.2.3\n\n" +
		"## Breaking Changes\n\n- 无。\n\n" +
		"## Features\n\n- 新增升级入口\n\n" +
		"## Bug Fixes\n\n- 无独立的用户可感知问题修复。\n\n" +
		"## Upgrade Notes\n\n\n" +
		"## Miscellaneous\n\n- 保留真实说明\n"

	got := HideEmptyReleaseNoteSections(notes)
	for _, hidden := range []string{"Breaking Changes", "Bug Fixes", "Upgrade Notes", "- 无。", "无独立的用户可感知问题修复"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("filtered notes still contain %q:\n%s", hidden, got)
		}
	}
	for _, visible := range []string{"# v1.2.3", "## Features", "新增升级入口", "## Miscellaneous", "保留真实说明"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("filtered notes missing %q:\n%s", visible, got)
		}
	}
}

func TestHideEmptyReleaseNoteSectionsHandlesAggregatedAndLegacyAliases(t *testing.T) {
	notes := "## v1.2.3\n\n# v1.2.3\n\n## 主要更新\n\n- 无\n\n## 修复\n\n- 修复断线重连\n\n## Miscellaneous\n\n- 无。\n\n---\n\n" +
		"## v1.2.2\n\n# v1.2.2\n\n## 升级说明\n\n- 无。\n"

	got := HideEmptyReleaseNoteSections(notes)
	if strings.Contains(got, "## 主要更新") || strings.Contains(got, "## Miscellaneous") || strings.Contains(got, "## 升级说明") {
		t.Fatalf("empty legacy sections remain:\n%s", got)
	}
	for _, visible := range []string{"## v1.2.3", "## v1.2.2", "## 修复", "修复断线重连"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("aggregated notes missing %q:\n%s", visible, got)
		}
	}
}

func TestHideEmptyReleaseNoteSectionsPreservesUnknownOrNonEmptyMarkdown(t *testing.T) {
	notes := "# Custom\n\n## Internal\n\n- 无。\n\n## Features\n\n普通说明\n"
	if got := HideEmptyReleaseNoteSections(notes); got != notes {
		t.Fatalf("non-empty and unknown markdown changed:\n got: %q\nwant: %q", got, notes)
	}
}
