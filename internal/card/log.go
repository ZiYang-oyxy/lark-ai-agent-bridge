package card

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type LogRenderer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewLogRenderer(w io.Writer) *LogRenderer {
	return &LogRenderer{w: w}
}

func (r *LogRenderer) Render(e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil {
		return nil
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(r.w, string(payload))
	return err
}
