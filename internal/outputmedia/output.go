package outputmedia

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"lark-agent-bridge/internal/card"
)

const (
	MaxImages           = 5
	MaxImageBytes int64 = 10 << 20
	NoReuse             = -1
)

type ReasonCode string

const (
	ReasonNotFound        ReasonCode = "file_not_found"
	ReasonOutsideWorkdir  ReasonCode = "outside_workdir"
	ReasonUnsupportedType ReasonCode = "unsupported_type"
	ReasonEmpty           ReasonCode = "empty_file"
	ReasonTooLarge        ReasonCode = "too_large"
	ReasonTooMany         ReasonCode = "too_many"
	ReasonReadFailed      ReasonCode = "read_failed"
	ReasonUploadFailed    ReasonCode = "upload_failed"
	ReasonReplyFailed     ReasonCode = "reply_failed"
)

type ResultState string

const (
	ResultSent   ResultState = "sent"
	ResultFailed ResultState = "failed"
)

type Result struct {
	State  ResultState
	Reason ReasonCode
}

type Item struct {
	Index     int
	Alt       string
	MIME      string
	Size      int64
	Reader    io.ReadSeeker
	ReuseOf   int
	Rejection ReasonCode
	file      *os.File
	digest    [sha256.Size]byte
}

type reference struct {
	segment int
	start   int
	end     int
	item    int
}

type Plan struct {
	segments []card.Segment
	items    []Item
	refs     []reference
}

func Prepare(workDir string, segments []card.Segment) *Plan {
	p := &Plan{segments: append([]card.Segment(nil), segments...)}
	resolvedWorkDir, workErr := resolveWorkDir(workDir)
	seen := make(map[[sha256.Size]byte]int)
	for segmentIndex, segment := range segments {
		if segment.Kind != card.SegmentText {
			continue
		}
		for _, parsed := range scanInlineImages(segment.Text) {
			if !localDestination(parsed.destination) {
				continue
			}
			itemIndex := len(p.items)
			alt := strings.TrimSpace(parsed.alt)
			if alt == "" {
				alt = fmt.Sprintf("图片 %d", itemIndex+1)
			}
			item := Item{Index: itemIndex, Alt: alt, ReuseOf: NoReuse}
			if workErr != nil {
				item.Rejection = ReasonReadFailed
			} else {
				prepareItem(resolvedWorkDir, parsed.destination, &item)
				if item.Rejection == "" {
					if first, ok := seen[item.digest]; ok {
						item.ReuseOf = first
						_ = item.file.Close()
						item.file = nil
						item.Reader = nil
					} else if len(seen) >= MaxImages {
						_ = item.file.Close()
						item.file = nil
						item.Reader = nil
						item.Rejection = ReasonTooMany
					} else {
						seen[item.digest] = itemIndex
					}
				}
			}
			p.items = append(p.items, item)
			p.refs = append(p.refs, reference{segment: segmentIndex, start: parsed.start, end: parsed.end, item: itemIndex})
		}
	}
	return p
}

func (p *Plan) HasImages() bool { return p != nil && len(p.items) > 0 }

func (p *Plan) Items() []Item {
	if p == nil {
		return nil
	}
	return append([]Item(nil), p.items...)
}

func (p *Plan) PendingSegments() []card.Segment {
	return p.render(func(item Item) string {
		return fmt.Sprintf("🖼️ %s（正在发送）", item.Alt)
	})
}

func (p *Plan) FinalSegments(results []Result) []card.Segment {
	return p.render(func(item Item) string {
		result := Result{State: ResultFailed, Reason: item.Rejection}
		if item.Index < len(results) {
			result = results[item.Index]
		}
		if result.State == ResultSent {
			return fmt.Sprintf("🖼️ %s（已作为图片发送）", item.Alt)
		}
		return fmt.Sprintf("⚠️ %s（图片发送失败：%s）", item.Alt, reasonText(result.Reason))
	})
}

func (p *Plan) Close() error {
	if p == nil {
		return nil
	}
	var first error
	for i := range p.items {
		if p.items[i].file != nil {
			if err := p.items[i].file.Close(); err != nil && first == nil {
				first = err
			}
			p.items[i].file = nil
			p.items[i].Reader = nil
		}
	}
	return first
}

func (p *Plan) render(replacement func(Item) string) []card.Segment {
	if p == nil {
		return nil
	}
	out := append([]card.Segment(nil), p.segments...)
	refsBySegment := make(map[int][]reference)
	for _, ref := range p.refs {
		refsBySegment[ref.segment] = append(refsBySegment[ref.segment], ref)
	}
	for segmentIndex, refs := range refsBySegment {
		var b strings.Builder
		cursor := 0
		for _, ref := range refs {
			b.WriteString(out[segmentIndex].Text[cursor:ref.start])
			b.WriteString(replacement(p.items[ref.item]))
			cursor = ref.end
		}
		b.WriteString(out[segmentIndex].Text[cursor:])
		out[segmentIndex].Text = b.String()
	}
	return out
}

func resolveWorkDir(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func prepareItem(workDir, destination string, item *Item) {
	path := destination
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			item.Rejection = ReasonNotFound
		} else {
			item.Rejection = ReasonReadFailed
		}
		return
	}
	rel, err := filepath.Rel(workDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		item.Rejection = ReasonOutsideWorkdir
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		item.Rejection = ReasonReadFailed
		return
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		_ = file.Close()
		item.Rejection = ReasonReadFailed
		return
	}
	item.Size = stat.Size()
	if item.Size == 0 {
		_ = file.Close()
		item.Rejection = ReasonEmpty
		return
	}
	if item.Size > MaxImageBytes {
		_ = file.Close()
		item.Rejection = ReasonTooLarge
		return
	}
	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		_ = file.Close()
		item.Rejection = ReasonReadFailed
		return
	}
	item.MIME = http.DetectContentType(header[:n])
	if !allowedMIME(item.MIME) {
		_ = file.Close()
		item.Rejection = ReasonUnsupportedType
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		item.Rejection = ReasonReadFailed
		return
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		item.Rejection = ReasonReadFailed
		return
	}
	copy(item.digest[:], hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		item.Rejection = ReasonReadFailed
		return
	}
	item.file = file
	item.Reader = file
}

func allowedMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func localDestination(destination string) bool {
	parsed, err := url.Parse(destination)
	return err == nil && parsed.Scheme == "" && destination != ""
}

type parsedImage struct {
	start       int
	end         int
	alt         string
	destination string
}

func scanInlineImages(text string) []parsedImage {
	var images []parsedImage
	for i := 0; i+4 <= len(text); i++ {
		if text[i] != '!' || text[i+1] != '[' || escapedAt(text, i) {
			continue
		}
		closeAlt := findUnescaped(text, i+2, ']')
		if closeAlt < 0 || closeAlt+1 >= len(text) || text[closeAlt+1] != '(' {
			continue
		}
		destStart := closeAlt + 2
		destination := ""
		end := -1
		if destStart < len(text) && text[destStart] == '<' {
			closeDest := findUnescaped(text, destStart+1, '>')
			if closeDest < 0 {
				continue
			}
			closeParen := closeDest + 1
			for closeParen < len(text) && (text[closeParen] == ' ' || text[closeParen] == '\t') {
				closeParen++
			}
			if closeParen >= len(text) || text[closeParen] != ')' {
				continue
			}
			destination = text[destStart+1 : closeDest]
			end = closeParen + 1
		} else {
			depth := 1
			for j := destStart; j < len(text); j++ {
				if escapedAt(text, j) {
					continue
				}
				switch text[j] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						destination = strings.TrimSpace(text[destStart:j])
						end = j + 1
						j = len(text)
					}
				}
			}
		}
		if end < 0 {
			continue
		}
		images = append(images, parsedImage{
			start: i, end: end,
			alt:         unescapeMarkdown(text[i+2 : closeAlt]),
			destination: unescapeMarkdown(destination),
		})
		i = end - 1
	}
	return images
}

func findUnescaped(text string, start int, target byte) int {
	for i := start; i < len(text); i++ {
		if text[i] == target && !escapedAt(text, i) {
			return i
		}
	}
	return -1
}

func escapedAt(text string, index int) bool {
	backslashes := 0
	for i := index - 1; i >= 0 && text[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func unescapeMarkdown(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

func reasonText(reason ReasonCode) string {
	switch reason {
	case ReasonNotFound:
		return "文件不存在"
	case ReasonOutsideWorkdir:
		return "路径不在工作目录内"
	case ReasonUnsupportedType:
		return "不支持的图片类型"
	case ReasonEmpty:
		return "图片为空"
	case ReasonTooLarge:
		return "文件超过 10 MB"
	case ReasonTooMany:
		return "每轮最多发送 5 张图片"
	case ReasonUploadFailed:
		return "上传失败"
	case ReasonReplyFailed:
		return "回复失败"
	default:
		return "读取失败"
	}
}
