package media

import "testing"

func TestValidateAllowlist(t *testing.T) {
	tests := []struct {
		name     string
		mime     string
		wantMIME string
		wantOK   bool
	}{
		{name: "a.jpg", mime: "image/jpeg", wantMIME: "image/jpeg", wantOK: true},
		{name: "a.JPEG", mime: "image/jpeg; name=a.jpeg", wantMIME: "image/jpeg", wantOK: true},
		{name: "a.png", mime: "image/png", wantMIME: "image/png", wantOK: true},
		{name: "a.webp", mime: "image/webp", wantMIME: "image/webp", wantOK: true},
		{name: "a.gif", mime: "image/gif", wantMIME: "image/gif", wantOK: true},
		{name: "a.txt", mime: "text/plain; charset=utf-8", wantMIME: "text/plain", wantOK: true},
		{name: "a.log", mime: "text/plain", wantMIME: "text/plain", wantOK: true},
		{name: "a.md", mime: "text/markdown", wantMIME: "text/markdown", wantOK: true},
		{name: "a.markdown", mime: "text/plain", wantMIME: "text/markdown", wantOK: true},
		{name: "a.json", mime: "application/json; charset=utf-8", wantMIME: "application/json", wantOK: true},
		{name: "a.csv", mime: "text/csv", wantMIME: "text/csv", wantOK: true},
		{name: "a.csv", mime: "text/plain", wantMIME: "text/csv", wantOK: true},
		{name: "a.pdf", mime: "application/pdf", wantOK: false},
		{name: "a.docx", mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", wantOK: false},
		{name: "a.xlsx", mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", wantOK: false},
		{name: "a.pptx", mime: "application/vnd.openxmlformats-officedocument.presentationml.presentation", wantOK: false},
		{name: "a.mp3", mime: "audio/mpeg", wantOK: false},
		{name: "a.mp4", mime: "video/mp4", wantOK: false},
		{name: "a.bin", mime: "application/octet-stream", wantOK: false},
		{name: "a.png", mime: "image/jpeg", wantOK: false},
		{name: "a.txt", mime: "text/csv", wantOK: false},
		{name: "a.json", mime: "text/plain", wantOK: false},
		{name: "a", mime: "image/png", wantOK: false},
		{name: "a.png", mime: "not a mime", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.mime, func(t *testing.T) {
			got, err := Validate(tt.name, tt.mime)
			if (err == nil) != tt.wantOK {
				t.Fatalf("Validate(%q, %q) err=%v, wantOK=%v", tt.name, tt.mime, err, tt.wantOK)
			}
			if tt.wantOK && got != tt.wantMIME {
				t.Fatalf("Validate(%q, %q) = %q, want %q", tt.name, tt.mime, got, tt.wantMIME)
			}
		})
	}
}
