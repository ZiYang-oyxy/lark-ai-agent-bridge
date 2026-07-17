package tmux

import (
	"context"
	"strings"
)

type PaneWatcher struct {
	Manager    *Manager
	WindowName string
	Last       string
	LastLines  int
}

func (w *PaneWatcher) Poll(ctx context.Context) (string, error) {
	if w.LastLines <= 0 {
		w.LastLines = 300
	}
	current, err := w.Manager.CapturePane(ctx, w.WindowName, w.LastLines)
	if err != nil {
		return "", err
	}
	delta := current
	if strings.HasPrefix(current, w.Last) {
		delta = current[len(w.Last):]
	}
	w.Last = current
	return delta, nil
}
