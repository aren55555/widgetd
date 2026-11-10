package host

import (
	"fmt"
	"regexp"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// A widget's icon is drawn on widgetd's own pages (the board, the admin
// pages), outside any sandbox, so it is checked here as data: only the
// shapes and geometry attributes Lucide uses, with values that are numbers
// and path commands. Pages build the SVG element by element from what passes;
// nothing in it is ever parsed as markup.

// iconAttrs are the attributes each shape may have.
var iconAttrs = map[string]map[string]bool{
	"path":     {"d": true},
	"circle":   {"cx": true, "cy": true, "r": true, "fill": true},
	"rect":     {"x": true, "y": true, "width": true, "height": true, "rx": true, "ry": true},
	"line":     {"x1": true, "y1": true, "x2": true, "y2": true},
	"ellipse":  {"cx": true, "cy": true, "rx": true, "ry": true},
	"polyline": {"points": true},
	"polygon":  {"points": true},
}

var (
	iconNameRE  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	iconValueRE = regexp.MustCompile(`^[0-9MmLlHhVvCcSsQqTtAaZz ,.eE+-]*$`)
)

const (
	maxIconShapes = 32
	maxIconValue  = 4096
)

// checkIcon reports what, if anything, makes ic unfit to draw.
func checkIcon(ic *widget.Icon) error {
	if ic == nil {
		return nil
	}
	if len(ic.Name) > 64 || !iconNameRE.MatchString(ic.Name) {
		return fmt.Errorf("icon: bad name %q", ic.Name)
	}
	if len(ic.Shapes) == 0 || len(ic.Shapes) > maxIconShapes {
		return fmt.Errorf("icon %s: %d shapes, want 1 to %d", ic.Name, len(ic.Shapes), maxIconShapes)
	}
	for _, s := range ic.Shapes {
		allowed, ok := iconAttrs[s.Tag]
		if !ok {
			return fmt.Errorf("icon %s: shape <%s> isn't allowed", ic.Name, s.Tag)
		}
		for k, v := range s.Attrs {
			if !allowed[k] {
				return fmt.Errorf("icon %s: <%s %s> isn't allowed", ic.Name, s.Tag, k)
			}
			if k == "fill" {
				if v != "none" && v != "currentColor" {
					return fmt.Errorf("icon %s: fill %q isn't allowed", ic.Name, v)
				}
				continue
			}
			if len(v) > maxIconValue || !iconValueRE.MatchString(v) {
				return fmt.Errorf("icon %s: <%s %s> has a value that isn't geometry", ic.Name, s.Tag, k)
			}
		}
	}
	return nil
}

// iconProto is ic for a page, or nil.
func iconProto(ic *widget.Icon) *widgetdv1.Icon {
	if ic == nil {
		return nil
	}
	out := &widgetdv1.Icon{Name: ic.Name}
	for _, s := range ic.Shapes {
		out.Shapes = append(out.Shapes, &widgetdv1.IconShape{Tag: s.Tag, Attrs: s.Attrs})
	}
	return out
}
