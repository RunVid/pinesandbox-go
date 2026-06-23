package coordinator

import (
	"encoding/json"
	"fmt"
	"time"
)

// Typed read results for the control (tabs) + drive (observe) tracks, mirroring the
// coordinator-api.yaml schemas (Tab, ObserveResult/ObservationSnapshot, Size).

// Tab is a browser tab in a session (spec Tab). No timestamps → decoded directly.
type Tab struct {
	TargetID string `json:"target_id"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Label    string `json:"label,omitempty"`
	Active   bool   `json:"active,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
}

// Size is a width/height pair in pixels (spec Size).
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Scroll is the page scroll offset (CSS px).
type Scroll struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Observation is the ObservationSnapshot (spec ObserveResult) — "what the screen looks
// like": a base64 screenshot plus the coordinate metadata (viewport / screenshot_size /
// device_scale_factor / scroll) needed to map bbox/pixel actions across DPR + resize.
// Elements is the affordance summary (an empty placeholder today, kept raw as it evolves).
type Observation struct {
	SchemaVersion     int
	ObservedAt        *time.Time
	Seq               int64
	PageEpoch         int64
	URL               string
	Screenshot        string // base64 PNG (empty when the request opts out)
	Viewport          Size
	ScreenshotSize    Size
	DeviceScaleFactor float64
	Scroll            Scroll
	Elements          []json.RawMessage
	Raw               json.RawMessage
}

type observationWire struct {
	SchemaVersion     int               `json:"schema_version"`
	ObservedAt        string            `json:"observed_at"`
	Seq               int64             `json:"seq"`
	PageEpoch         int64             `json:"page_epoch"`
	URL               string            `json:"url"`
	Screenshot        string            `json:"screenshot"`
	Viewport          Size              `json:"viewport"`
	ScreenshotSize    Size              `json:"screenshot_size"`
	DeviceScaleFactor float64           `json:"device_scale_factor"`
	Scroll            Scroll            `json:"scroll"`
	Elements          []json.RawMessage `json:"elements"`
}

func (w *observationWire) toObservation() *Observation {
	return &Observation{
		SchemaVersion:     w.SchemaVersion,
		ObservedAt:        parseTime(w.ObservedAt),
		Seq:               w.Seq,
		PageEpoch:         w.PageEpoch,
		URL:               w.URL,
		Screenshot:        w.Screenshot,
		Viewport:          w.Viewport,
		ScreenshotSize:    w.ScreenshotSize,
		DeviceScaleFactor: w.DeviceScaleFactor,
		Scroll:            w.Scroll,
		Elements:          w.Elements,
	}
}

func parseObservation(raw json.RawMessage) (*Observation, error) {
	var w observationWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode observation: %w", err)
	}
	o := w.toObservation()
	o.Raw = raw
	return o, nil
}

func parseTab(raw json.RawMessage) (*Tab, error) {
	var t Tab
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode tab: %w", err)
	}
	return &t, nil
}

func parseTabs(raw json.RawMessage) ([]Tab, error) {
	var tabs []Tab
	if err := json.Unmarshal(raw, &tabs); err != nil {
		return nil, fmt.Errorf("pinesandbox: decode tabs: %w", err)
	}
	return tabs, nil
}
