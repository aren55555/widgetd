package server

import (
	"context"
	"net/url"
	"sort"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Choices offers the account's calendars for the "calendars" field, so the
// settings form shows a list to pick from rather than asking for ids.
func Choices(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
	var out []widget.Choice
	page := ""
	for range 10 {
		q := url.Values{"fields": {"nextPageToken,items(id,summary,summaryOverride,primary)"}, "maxResults": {"250"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID              string `json:"id"`
				Summary         string `json:"summary"`
				SummaryOverride string `json:"summaryOverride"`
				Primary         bool   `json:"primary"`
			} `json:"items"`
		}
		if err := get(ctx, h, api+"/users/me/calendarList?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, c := range resp.Items {
			name := c.SummaryOverride
			if name == "" {
				name = c.Summary
			}
			if c.Primary {
				// "primary" rather than the id, so the setting survives the
				// account being swapped for another.
				out = append(out, widget.Choice{Value: "primary", Label: name + " (primary)"})
				continue
			}
			out = append(out, widget.Choice{Value: c.ID, Label: name})
		}
		if page = resp.NextPageToken; page == "" {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Value == "primary") != (out[j].Value == "primary") {
			return out[i].Value == "primary"
		}
		return out[i].Label < out[j].Label
	})
	return map[string][]widget.Choice{"calendars": out}, nil
}
