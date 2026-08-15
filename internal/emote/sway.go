package emote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	swayCorrectionTimeout = 2 * time.Second
	swayWindowSettleTime  = 100 * time.Millisecond
)

type swayRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type swayOutput struct {
	Active  bool     `json:"active"`
	Focused bool     `json:"focused"`
	Rect    swayRect `json:"rect"`
}

type swayNode struct {
	Type          string     `json:"type"`
	AppID         string     `json:"app_id"`
	PID           int        `json:"pid"`
	Rect          swayRect   `json:"rect"`
	Nodes         []swayNode `json:"nodes"`
	FloatingNodes []swayNode `json:"floating_nodes"`
}

type swayOverlay struct {
	AppID  string
	Rect   swayRect
	Origin swayRect
}

type swayCorrector struct {
	executable  string
	origin      swayRect
	corrected   map[string]struct{}
	identifiers map[string]string
	trace       func(string)
	run         func(time.Duration, ...string) ([]byte, error)
	settle      time.Duration
}

type swayObservation struct {
	rect   swayRect
	origin swayRect
	since  time.Time
}

func newSwayCorrector(output string, getenv func(string) string, trace func(string)) (*swayCorrector, error) {
	if output != "wayland" || getenv("SWAYSOCK") == "" {
		return nil, nil
	}
	executable, err := exec.LookPath("swaymsg")
	if err != nil {
		return nil, errors.New("locate swaymsg")
	}
	corrector := &swayCorrector{executable: executable, corrected: make(map[string]struct{}), identifiers: make(map[string]string), trace: trace, settle: swayWindowSettleTime}
	corrector.run = corrector.runCommand
	data, err := corrector.run(500*time.Millisecond, "-t", "get_outputs", "-r")
	if err != nil {
		return nil, errors.New("query Sway outputs")
	}
	var outputs []swayOutput
	if json.Unmarshal(data, &outputs) != nil {
		return nil, errors.New("decode Sway outputs")
	}
	active := 0
	found := false
	for _, item := range outputs {
		if item.Active {
			active++
		}
		if item.Focused {
			corrector.origin = item.Rect
			found = true
		}
	}
	if active < 2 {
		return nil, nil
	}
	if !found {
		return nil, errors.New("identify focused Sway output")
	}
	traceUeberzug(trace, "Sway multi-output correction ready: origin_x=%d origin_y=%d", corrector.origin.X, corrector.origin.Y)
	return corrector, nil
}

func (c *swayCorrector) runCommand(timeout time.Duration, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	data, err := exec.CommandContext(ctx, c.executable, arguments...).Output()
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c *swayCorrector) Correct(pid int, identifier string, expected int, requireNew bool) error {
	if c == nil || expected == 0 {
		return nil
	}
	deadline := time.Now().Add(swayCorrectionTimeout)
	correctedNew := false
	observed := make(map[string]swayObservation)
	concealed := make(map[string]struct{})
	for time.Now().Before(deadline) {
		data, err := c.run(300*time.Millisecond, "-t", "get_tree", "-r")
		if err != nil {
			return errors.New("query Sway tree")
		}
		var tree swayNode
		if json.Unmarshal(data, &tree) != nil {
			return errors.New("decode Sway tree")
		}
		overlays := collectSwayOverlays(tree, pid)
		present := make(map[string]struct{}, len(overlays))
		for _, overlay := range overlays {
			present[overlay.AppID] = struct{}{}
			if _, ok := c.corrected[overlay.AppID]; ok {
				continue
			}
			if _, hidden := concealed[overlay.AppID]; !hidden {
				command := fmt.Sprintf(`[app_id="%s"] opacity set 0`, overlay.AppID)
				traceUeberzug(c.trace, "Sway placement concealed: app_id=%s", sanitizeUeberzugIdentifier(overlay.AppID))
				if _, err = c.run(300*time.Millisecond, command); err != nil {
					return errors.New("conceal Sway overlay before placement")
				}
				concealed[overlay.AppID] = struct{}{}
			}
			now := time.Now()
			observation, seen := observed[overlay.AppID]
			if !seen || observation.rect != overlay.Rect || observation.origin != overlay.Origin {
				observed[overlay.AppID] = swayObservation{rect: overlay.Rect, origin: overlay.Origin, since: now}
				continue
			}
			if now.Sub(observation.since) < c.settle {
				continue
			}
			x := overlay.Rect.X - overlay.Origin.X + c.origin.X
			y := overlay.Rect.Y - overlay.Origin.Y + c.origin.Y
			command := fmt.Sprintf(`[app_id="%s"] move absolute position %d %d`, overlay.AppID, x, y)
			traceUeberzug(c.trace, "Sway placement correction: app_id=%s x=%d y=%d", sanitizeUeberzugIdentifier(overlay.AppID), x, y)
			if _, err = c.run(300*time.Millisecond, command); err != nil {
				return errors.New("correct Sway overlay position")
			}
			command = fmt.Sprintf(`[app_id="%s"] opacity set 1`, overlay.AppID)
			if _, err = c.run(300*time.Millisecond, command); err != nil {
				return errors.New("reveal corrected Sway overlay")
			}
			c.corrected[overlay.AppID] = struct{}{}
			c.identifiers[identifier] = overlay.AppID
			correctedNew = true
		}
		for appID := range c.corrected {
			if _, ok := present[appID]; !ok {
				delete(c.corrected, appID)
			}
		}
		if len(overlays) == expected {
			allCorrected := true
			for _, overlay := range overlays {
				if _, ok := c.corrected[overlay.AppID]; !ok {
					allCorrected = false
					break
				}
			}
			if allCorrected && (!requireNew || correctedNew) {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("Sway overlay did not become ready")
}

// MoveRows keeps an existing Wayland surface alive while its terminal row
// changes. Re-sending an Überzug++ add for the same identifier destroys and
// recreates the surface, which causes a visible disappear/reappear cycle.
func (c *swayCorrector) MoveRows(pid int, identifier string, rows int) (bool, error) {
	if c == nil || rows == 0 {
		return rows == 0, nil
	}
	appID, ok := c.identifiers[identifier]
	if !ok {
		return false, nil
	}
	data, err := c.run(300*time.Millisecond, "-t", "get_tree", "-r")
	if err != nil {
		return false, errors.New("query Sway tree")
	}
	var tree swayNode
	if json.Unmarshal(data, &tree) != nil {
		return false, errors.New("decode Sway tree")
	}
	for _, overlay := range collectSwayOverlays(tree, pid) {
		if overlay.AppID != appID {
			continue
		}
		x := overlay.Rect.X
		y := overlay.Rect.Y + rows*overlay.Rect.Height
		command := fmt.Sprintf(`[app_id="%s"] move absolute position %d %d`, appID, x, y)
		traceUeberzug(c.trace, "Sway row move: identifier=%s app_id=%s rows=%d x=%d y=%d", sanitizeUeberzugIdentifier(identifier), sanitizeUeberzugIdentifier(appID), rows, x, y)
		if _, err = c.run(300*time.Millisecond, command); err != nil {
			return false, errors.New("move existing Sway overlay")
		}
		return true, nil
	}
	delete(c.identifiers, identifier)
	delete(c.corrected, appID)
	return false, nil
}

func (c *swayCorrector) Forget(identifier string) {
	if c == nil {
		return
	}
	if appID, ok := c.identifiers[identifier]; ok {
		delete(c.corrected, appID)
		delete(c.identifiers, identifier)
	}
}

func collectSwayOverlays(root swayNode, pid int) []swayOverlay {
	result := make([]swayOverlay, 0)
	var visit func(swayNode, swayRect)
	visit = func(node swayNode, output swayRect) {
		// Sway's floating `move position` coordinates are relative to the
		// workspace, whose origin may differ from the output because of a
		// top bar. Keep the output as a fallback for unusual trees.
		if node.Type == "output" || (node.Type == "workspace" && node.Rect.Width > 0 && node.Rect.Height > 0) {
			output = node.Rect
		}
		if node.PID == pid && strings.HasPrefix(node.AppID, "ueberzugpp_") && node.Rect.Width > 0 && node.Rect.Height > 0 {
			result = append(result, swayOverlay{AppID: node.AppID, Rect: node.Rect, Origin: output})
		}
		for _, child := range node.Nodes {
			visit(child, output)
		}
		for _, child := range node.FloatingNodes {
			visit(child, output)
		}
	}
	visit(root, swayRect{})
	return result
}
