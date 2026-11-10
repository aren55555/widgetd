package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

const api = "https://www.googleapis.com/calendar/v3"

// gEvent is the part of a Calendar API event the widget reads.
type gEvent struct {
	ICalUID      string `json:"iCalUID"`
	Status       string `json:"status"` // "confirmed", "tentative", "cancelled"
	Summary      string `json:"summary"`
	Location     string `json:"location"`
	Description  string `json:"description"`
	HangoutLink  string `json:"hangoutLink"`
	Transparency string `json:"transparency"` // "transparent": shown as free
	EventType    string `json:"eventType"`    // "default", "workingLocation", "outOfOffice", ...
	Start        gTime  `json:"start"`
	End          gTime  `json:"end"`
	Organizer    struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Self        bool   `json:"self"`
	} `json:"organizer"`
	Attendees      []gAttendee `json:"attendees"`
	ConferenceData struct {
		EntryPoints []struct {
			EntryPointType string `json:"entryPointType"`
			URI            string `json:"uri"`
		} `json:"entryPoints"`
	} `json:"conferenceData"`
}

type gAttendee struct {
	DisplayName    string `json:"displayName"`
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus"` // "needsAction", "declined", "tentative", "accepted"
	Self           bool   `json:"self"`
	Organizer      bool   `json:"organizer"`
	Resource       bool   `json:"resource"` // a room
}

// gTime is a timed event's instant (DateTime, RFC 3339 with its offset) or
// an all-day event's date (Date, "2006-01-02"; the end date is exclusive).
type gTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

// Only what build reads, so Google sends a few KB rather than everything.
const (
	fieldsBusy = "nextPageToken,items(iCalUID,status,transparency,eventType,start,end,attendees(self,responseStatus))"
	fieldsFull = "nextPageToken,items(iCalUID,status,summary,location,description,hangoutLink,transparency,eventType," +
		"start,end,organizer(displayName,email,self)," +
		"attendees(displayName,email,responseStatus,self,organizer,resource),conferenceData(entryPoints(entryPointType,uri)))"
)

// events reads one calendar's events in [from, to), with repeating events
// expanded into their occurrences.
func events(ctx context.Context, h widget.Host, calendar string, from, to time.Time, fields string) ([]gEvent, error) {
	var all []gEvent
	page := ""
	for range 20 { // 20 pages of 2500 is far more than two weeks holds
		q := url.Values{
			"singleEvents": {"true"},
			"timeMin":      {from.UTC().Format(time.RFC3339)},
			"timeMax":      {to.UTC().Format(time.RFC3339)},
			"maxResults":   {"2500"},
			"fields":       {fields},
		}
		if page != "" {
			q.Set("pageToken", page)
		}
		var resp struct {
			NextPageToken string   `json:"nextPageToken"`
			Items         []gEvent `json:"items"`
		}
		if err := get(ctx, h, api+"/calendars/"+url.PathEscape(calendar)+"/events?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if page = resp.NextPageToken; page == "" {
			return all, nil
		}
	}
	return all, nil
}

// get reads a signed Calendar API URL as JSON.
func get(ctx context.Context, h widget.Host, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := h.Fetch(widget.Authorize(req))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return &apiError{status: resp.StatusCode, msg: e.Error.Message}
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// apiError is a Calendar API reply other than 200.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("google calendar: %d %s", e.status, http.StatusText(e.status))
	}
	return fmt.Sprintf("google calendar: %d %s: %s", e.status, http.StatusText(e.status), e.msg)
}

// gone is a calendar that no longer exists, or isn't shared with the
// account any more.
func gone(err error) bool {
	var e *apiError
	return errors.As(err, &e) && (e.status == http.StatusNotFound || e.status == http.StatusGone)
}
