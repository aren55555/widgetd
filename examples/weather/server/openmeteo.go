package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	forecastAPI  = "https://api.open-meteo.com/v1/forecast"
	airAPI       = "https://air-quality-api.open-meteo.com/v1/air-quality"
	geocodingAPI = "https://geocoding-api.open-meteo.com/v1"
)

// days is how many days the card shows: today and the three after it. One
// more is asked for, so the last day's end (the next local midnight) is
// known rather than guessed.
const days = 4

// StatusError is a reply from Open-Meteo other than 200.
type StatusError struct {
	URL    string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status)
}

// errNoPlace is a geocoding id Open-Meteo doesn't know.
var errNoPlace = errors.New("no such place")

// get fetches u through the host and decodes its JSON into dst.
func get(ctx context.Context, h widget.Host, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return fmt.Errorf("weather: %w", err)
	}
	resp, err := h.Fetch(req)
	if err != nil {
		return fmt.Errorf("weather: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only; nothing to report
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body) // drain, so the connection is reused
		path := u
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		return &StatusError{URL: path, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("weather: decoding %s: %w", u, err)
	}
	return nil
}

// place is a geocoding result: one town or city.
type place struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Admin1    string  `json:"admin1"`
	Country   string  `json:"country"`
}

// label is how the settings form lists a place: "Springfield, Illinois,
// United States".
func (p place) label() string {
	parts := []string{p.Name}
	for _, s := range []string{p.Admin1, p.Country} {
		if s != "" && s != parts[len(parts)-1] {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// search finds places by name, most populous first (Open-Meteo's order).
func search(ctx context.Context, h widget.Host, name string) ([]place, error) {
	q := url.Values{"name": {name}, "count": {"10"}, "language": {"en"}, "format": {"json"}}
	var resp struct {
		Results []place `json:"results"`
	}
	if err := get(ctx, h, geocodingAPI+"/search?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// lookup finds a place by its geocoding id.
func lookup(ctx context.Context, h widget.Host, id string) (place, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return place{}, errNoPlace
	}
	var p place
	err := get(ctx, h, geocodingAPI+"/get?id="+id, &p)
	var status *StatusError
	switch {
	case errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusBadRequest):
		return place{}, errNoPlace
	case err != nil:
		return place{}, err
	case p.ID == 0:
		return place{}, errNoPlace
	}
	return p, nil
}

// forecast is the part of Open-Meteo's forecast reply the card uses, asked
// for with timeformat=unixtime and timezone=auto: every time is Unix
// seconds, and days run midnight to midnight at the place. Any value may be
// null.
type forecast struct {
	Timezone string `json:"timezone"`
	Current  struct {
		Temperature *float64 `json:"temperature_2m"`
		WeatherCode *int     `json:"weather_code"`
		IsDay       *int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time       []int64    `json:"time"`
		Precip     []*float64 `json:"precipitation"`
		PrecipProb []*float64 `json:"precipitation_probability"`
	} `json:"hourly"`
	Daily struct {
		Time        []int64    `json:"time"`
		WeatherCode []*int     `json:"weather_code"`
		High        []*float64 `json:"temperature_2m_max"`
		Low         []*float64 `json:"temperature_2m_min"`
		Sunrise     []*int64   `json:"sunrise"`
		Sunset      []*int64   `json:"sunset"`
		Precip      []*float64 `json:"precipitation_sum"`
		PrecipProb  []*float64 `json:"precipitation_probability_max"`
		Wind        []*float64 `json:"wind_speed_10m_max"`
		Gust        []*float64 `json:"wind_gusts_10m_max"`
		WindFrom    []*float64 `json:"wind_direction_10m_dominant"`
		Humidity    []*float64 `json:"relative_humidity_2m_mean"`
	} `json:"daily"`
}

func readForecast(ctx context.Context, h widget.Host, p place) (*forecast, error) {
	q := url.Values{
		"latitude":        {strconv.FormatFloat(p.Latitude, 'f', 4, 64)},
		"longitude":       {strconv.FormatFloat(p.Longitude, 'f', 4, 64)},
		"current":         {"temperature_2m,weather_code,is_day"},
		"hourly":          {"precipitation,precipitation_probability"},
		"daily":           {"weather_code,temperature_2m_max,temperature_2m_min,sunrise,sunset,precipitation_sum,precipitation_probability_max,wind_speed_10m_max,wind_gusts_10m_max,wind_direction_10m_dominant,relative_humidity_2m_mean"},
		"timezone":        {"auto"},
		"timeformat":      {"unixtime"},
		"forecast_days":   {strconv.Itoa(days + 1)},
		"wind_speed_unit": {"ms"},
	}
	var f forecast
	if err := get(ctx, h, forecastAPI+"?"+q.Encode(), &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// readAQI is the place's current US AQI, or nil if Open-Meteo has none.
func readAQI(ctx context.Context, h widget.Host, p place) (*float64, error) {
	q := url.Values{
		"latitude":   {strconv.FormatFloat(p.Latitude, 'f', 4, 64)},
		"longitude":  {strconv.FormatFloat(p.Longitude, 'f', 4, 64)},
		"current":    {"us_aqi"},
		"timeformat": {"unixtime"},
	}
	var resp struct {
		Current struct {
			USAQI *float64 `json:"us_aqi"`
		} `json:"current"`
	}
	if err := get(ctx, h, airAPI+"?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return resp.Current.USAQI, nil
}
