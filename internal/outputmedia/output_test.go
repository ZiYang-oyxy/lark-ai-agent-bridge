package outputmedia

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lark-agent-bridge/internal/card"
)

func TestPrepareExtractsOnlyInlineLocalImagesFromTextSegments(t *testing.T) {
	work := t.TempDir()
	writePNG(t, filepath.Join(work, "plot (1).png"))

	plan := Prepare(work, []card.Segment{
		{Kind: card.SegmentThought, Text: "![secret](plot.png)"},
		{Kind: card.SegmentText, Text: "before ![趋势图](<plot (1).png>) after"},
		{Kind: card.SegmentTool, Text: "![tool](plot.png)"},
	})
	defer plan.Close()

	if got := len(plan.Items()); got != 1 {
		t.Fatalf("items=%d, want 1", got)
	}
	if got := plan.Items()[0].Alt; got != "趋势图" {
		t.Fatalf("alt=%q", got)
	}
	if got := plan.PendingSegments()[1].Text; got != "before 🖼️ 趋势图（正在发送） after" {
		t.Fatalf("pending=%q", got)
	}
}

func TestPrepareLeavesUnsupportedImageSyntaxUntouched(t *testing.T) {
	segments := []card.Segment{{Kind: card.SegmentText, Text: strings.Join([]string{
		"![web](https://example.com/a.png)",
		"![data](data:image/png;base64,AAAA)",
		"![file](file:///tmp/a.png)",
		"![ref][image-id]",
		`<img src="a.png">`,
	}, "\n")}}

	plan := Prepare(t.TempDir(), segments)
	defer plan.Close()
	if plan.HasImages() {
		t.Fatalf("unexpected items: %#v", plan.Items())
	}
	if got := plan.PendingSegments()[0].Text; got != segments[0].Text {
		t.Fatalf("text changed:\n%s", got)
	}
}

func TestPlanRendersStablePendingAndFinalStatuses(t *testing.T) {
	work := t.TempDir()
	writePNG(t, filepath.Join(work, "one.png"))
	writePNG(t, filepath.Join(work, "two.png"))
	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![](one.png) and ![二](two.png)"}})
	defer plan.Close()

	if got := plan.PendingSegments()[0].Text; got != "🖼️ 图片 1（正在发送） and 🖼️ 二（正在发送）" {
		t.Fatalf("pending=%q", got)
	}
	got := plan.FinalSegments([]Result{
		{State: ResultSent},
		{State: ResultFailed, Reason: ReasonTooLarge},
	})[0].Text
	want := "🖼️ 图片 1（已作为图片发送） and ⚠️ 二（图片发送失败：文件超过 10 MB）"
	if got != want {
		t.Fatalf("final=%q, want %q", got, want)
	}
}

func TestPrepareRejectsSymlinkOutsideWorkdir(t *testing.T) {
	work, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "secret.png")
	writePNG(t, target)
	if err := os.Symlink(target, filepath.Join(work, "escape.png")); err != nil {
		t.Fatal(err)
	}

	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![x](escape.png)"}})
	defer plan.Close()
	if got := plan.Items()[0].Rejection; got != ReasonOutsideWorkdir {
		t.Fatalf("reason=%s", got)
	}
}

func TestPrepareRejectsPrefixCollisionAndNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	outside := filepath.Join(root, "work-secret")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(outside, "secret.png"))

	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![x](../work-secret/secret.png) ![dir](.)"}})
	defer plan.Close()
	items := plan.Items()
	if items[0].Rejection != ReasonOutsideWorkdir {
		t.Fatalf("prefix collision reason=%s", items[0].Rejection)
	}
	if items[1].Rejection != ReasonReadFailed {
		t.Fatalf("directory reason=%s", items[1].Rejection)
	}
}

func TestPrepareEnforcesSizeTypeAndCountLimits(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "empty.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fake.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSizedPNG(t, filepath.Join(work, "large.png"), MaxImageBytes+1)
	for i := 1; i <= 6; i++ {
		writePNGWithSuffix(t, filepath.Join(work, "image-"+string(rune('0'+i))+".png"), byte(i))
	}
	text := "![empty](empty.png) ![fake](fake.png) ![large](large.png)"
	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: text}})
	defer plan.Close()
	items := plan.Items()
	if items[0].Rejection != ReasonEmpty || items[1].Rejection != ReasonUnsupportedType || items[2].Rejection != ReasonTooLarge {
		t.Fatalf("reasons=%v,%v,%v", items[0].Rejection, items[1].Rejection, items[2].Rejection)
	}

	countPlan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![1](image-1.png) ![2](image-2.png) ![3](image-3.png) ![4](image-4.png) ![5](image-5.png) ![6](image-6.png)"}})
	defer countPlan.Close()
	if got := countPlan.Items()[5].Rejection; got != ReasonTooMany {
		t.Fatalf("sixth reason=%s", got)
	}
}

func TestPrepareDeduplicatesContentAndRewindsReader(t *testing.T) {
	work := t.TempDir()
	writePNG(t, filepath.Join(work, "one.png"))
	writePNG(t, filepath.Join(work, "copy.png"))
	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![one](one.png) ![copy](copy.png)"}})
	items := plan.Items()
	if items[0].ReuseOf != NoReuse || items[1].ReuseOf != 0 {
		t.Fatalf("reuse=%d,%d", items[0].ReuseOf, items[1].ReuseOf)
	}
	prefix := make([]byte, 8)
	if _, err := io.ReadFull(items[0].Reader, prefix); err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("prefix=%q", prefix)
	}
	if items[1].Reader != nil {
		t.Fatal("duplicate content retained an unnecessary reader")
	}
	if err := plan.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := items[0].Reader.Seek(0, io.SeekStart); err == nil {
		t.Fatal("reader remained usable after Close")
	}
}

func TestPrepareDoesNotCountRepeatedReferenceAgainstImageLimit(t *testing.T) {
	work := t.TempDir()
	writePNG(t, filepath.Join(work, "one.png"))
	text := "![1](one.png) ![2](one.png) ![3](one.png) ![4](one.png) ![5](one.png) ![6](one.png)"
	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: text}})
	defer plan.Close()
	for i, item := range plan.Items() {
		if item.Rejection != "" {
			t.Fatalf("item %d rejected: %s", i, item.Rejection)
		}
		if i > 0 && item.ReuseOf != 0 {
			t.Fatalf("item %d reuse=%d", i, item.ReuseOf)
		}
	}
}

func TestPrepareAcceptsExactTenMiBBoundary(t *testing.T) {
	work := t.TempDir()
	writeSizedPNG(t, filepath.Join(work, "boundary.png"), MaxImageBytes)
	plan := Prepare(work, []card.Segment{{Kind: card.SegmentText, Text: "![x](boundary.png)"}})
	defer plan.Close()
	item := plan.Items()[0]
	if item.Rejection != "" || item.Size != MaxImageBytes || item.MIME != "image/png" {
		t.Fatalf("item=%+v", item)
	}
}

func writePNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePNGWithSuffix(t *testing.T, path string, suffix byte) {
	t.Helper()
	writePNG(t, path)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{suffix}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeSizedPNG(t *testing.T, path string, size int64) {
	t.Helper()
	writePNG(t, path)
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}
