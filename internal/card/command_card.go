package card

import "fmt"

// command_card.go holds reusable structural helpers for command-style CardKit
// 2.0 cards (/help, /config, /local-config). They produce stable element maps so
// the three command surfaces share one visual language: bordered sections,
// labelled fields, equal-width button rows and small grey notes.

// sectionElement wraps a group of body elements in a grey rounded container with
// a title. It reuses the collapsible_panel border style but is always expanded
// and its fold interaction is disabled, so it reads as a static bordered section
// rather than a collapsible one.
func sectionElement(title string, body []map[string]any) map[string]any {
	return map[string]any{
		"tag":              "collapsible_panel",
		"expanded":         true,
		"vertical_spacing": "8px",
		"padding":          "8px 8px 8px 8px",
		"header": map[string]any{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": title,
			},
			"vertical_align": "center",
			"padding":        "4px 0px 4px 8px",
			"width":          "fill",
			// No fold icon: this section is a static visual group, not an
			// interactive collapsible.
		},
		"border": map[string]string{
			"color":         "grey",
			"corner_radius": "5px",
		},
		"elements": body,
	}
}

// fieldElements renders a single labelled form field as an ordered slice:
//   - a bold markdown label (element_id id+"_label")
//   - an optional one-line grey hint (element_id id+"_hint", text_size
//     notation); omitted when empty
//   - the control (select/button/etc.); appended only when non-nil
//
// The id prefix gives every field a stable element_id, which matters for the
// /config surface that calls this many times.
func fieldElements(id, label, hint string, control map[string]any) []map[string]any {
	elements := []map[string]any{
		markdownElement(id+"_label", fmt.Sprintf("**%s**", label)),
	}
	if hint != "" {
		elements = append(elements, noteElement(id+"_hint", hint))
	}
	if control != nil {
		elements = append(elements, control)
	}
	return elements
}

// buttonRowElements lays buttons out in an equal-width column_set row. The first
// command is styled primary, while close actions and later buttons stay default.
// Each button carries a callback behavior tied to the session. Returns nil when
// there are no buttons.
func buttonRowElements(buttons []Action, sessionID string) map[string]any {
	if len(buttons) == 0 {
		return nil
	}
	columns := make([]any, 0, len(buttons))
	for i, action := range buttons {
		buttonType := "default"
		if i == 0 && action.ID != "card.close" && action.ID != "config.close" {
			buttonType = "primary"
		}
		button := map[string]any{
			"tag":      "button",
			"text":     map[string]any{"tag": "plain_text", "content": action.Label},
			"type":     buttonType,
			"width":    "fill",
			"size":     "medium",
			"disabled": action.Disabled,
		}
		if !action.Disabled && action.URL != "" {
			button["behaviors"] = []any{map[string]any{"type": "open_url", "default_url": action.URL}}
		} else if !action.Disabled {
			button["behaviors"] = callbackBehavior(sessionID, action.ID, action.Value)
		}
		columns = append(columns, map[string]any{
			"tag":            "column",
			"width":          "weighted",
			"weight":         1,
			"vertical_align": "top",
			"elements":       []any{button},
		})
	}
	return map[string]any{
		"tag":                "column_set",
		"horizontal_spacing": "8px",
		"columns":            columns,
	}
}

func closeButtonRow(sessionID string) map[string]any {
	return buttonRowElements([]Action{{ID: "card.close", Label: "关闭"}}, sessionID)
}

// noteElement renders a small grey markdown line (text_size notation), used for
// hints and footnotes across the command cards.
func noteElement(id, content string) map[string]any {
	el := markdownElement(id, content)
	el["text_size"] = "notation"
	return el
}
