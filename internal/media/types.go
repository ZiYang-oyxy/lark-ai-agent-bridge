package media

// Ref identifies one resource attached to a Feishu message.
type Ref struct {
	MessageID string `json:"message_id"`
	FileKey   string `json:"file_key"`
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
}

// Attachment is a validated resource stored in the local media cache.
type Attachment struct {
	Ref
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Failure describes one resource that could not be accepted.
type Failure struct {
	Ref
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Limits bounds attachment and cache resource use.
type Limits struct {
	MaxFileBytes    int64
	MaxBatchBytes   int64
	MaxFiles        int
	CacheQuotaBytes int64
}
