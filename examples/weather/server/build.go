package server

import (
	"math"
	"time"

	pb "github.com/aren55555/widgetd/examples/weather/gen/weather/v1"
)

// condition folds a WMO weather code into what the card has an icon for.
// https://open-meteo.com/en/docs#weather_variable_documentation
func condition(code *int) pb.Condition {
	if code == nil {
		return pb.Condition_CONDITION_UNSPECIFIED
	}
	switch *code {
	case 0, 1:
		return pb.Condition_CONDITION_CLEAR
	case 2:
		return pb.Condition_CONDITION_PARTLY_CLOUDY
	case 3:
		return pb.Condition_CONDITION_CLOUDY
	case 45, 48:
		return pb.Condition_CONDITION_FOG
	case 51, 53, 55, 56, 57:
		return pb.Condition_CONDITION_DRIZZLE
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return pb.Condition_CONDITION_RAIN
	case 71, 73, 75, 77, 85, 86:
		return pb.Condition_CONDITION_SNOW
	case 95, 96, 99:
		return pb.Condition_CONDITION_THUNDERSTORM
	}
	return pb.Condition_CONDITION_UNSPECIFIED
}

// at is s[i], or nil past its end: Open-Meteo's arrays can come up short.
func at[T any](s []*T, i int) *T {
	if i < len(s) {
		return s[i]
	}
	return nil
}

// num is *v clamped to [lo, hi], or 0 when unset or not a number, so the
// state always passes its rules.
func num(v *float64, lo, hi float64) float32 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0
	}
	return float32(math.Max(lo, math.Min(hi, *v)))
}

func percent(v *float64) uint32 { return uint32(math.Round(float64(num(v, 0, 100)))) }

func unix(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// build turns Open-Meteo's replies into the card. aqi may be nil.
func build(now time.Time, name string, units pb.Units, f *forecast, aqi *float64) *pb.State {
	st := &pb.State{
		Name:        name,
		TimeZone:    f.Timezone,
		Temperature: units,
		Updated:     now.Unix(),
		Current: &pb.Current{
			TemperatureC: num(f.Current.Temperature, -100, 70),
			Condition:    condition(f.Current.WeatherCode),
			IsDay:        f.Current.IsDay == nil || *f.Current.IsDay == 1,
		},
	}
	if lat, lon := f.Latitude, f.Longitude; lat != nil && lon != nil &&
		math.Abs(*lat) <= 90 && math.Abs(*lon) <= 180 {
		st.Latitude, st.Longitude = lat, lon
	}
	if aqi != nil && !math.IsNaN(*aqi) {
		v := uint32(math.Round(math.Max(0, math.Min(999, *aqi))))
		st.Current.UsAqi = &v
	}

	d := f.Daily
	for i := 0; i < len(d.Time) && i < days; i++ {
		start := d.Time[i]
		end := start + 24*60*60 // only if Open-Meteo sent no next day
		if i+1 < len(d.Time) {
			end = d.Time[i+1]
		}
		day := &pb.Day{
			Start:        start,
			End:          end,
			Condition:    condition(at(d.WeatherCode, i)),
			HighC:        num(at(d.High, i), -100, 70),
			LowC:         num(at(d.Low, i), -100, 70),
			PrecipChance: percent(at(d.PrecipProb, i)),
			PrecipMm:     num(at(d.Precip, i), 0, 2000),
			Sunrise:      unix(at(d.Sunrise, i)),
			Sunset:       unix(at(d.Sunset, i)),
			Humidity:     percent(at(d.Humidity, i)),
			WindMps:      num(at(d.Wind, i), 0, 200),
			GustMps:      num(at(d.Gust, i), 0, 200),
			WindFromDeg:  uint32(math.Mod(math.Round(float64(num(at(d.WindFrom, i), 0, 360))), 360)),
		}
		// The day's hours, in order from its midnight.
		hr := f.Hourly
		for j, t := range hr.Time {
			if t < start || t >= end || len(day.HourlyPrecipMm) == 25 {
				continue
			}
			day.HourlyPrecipMm = append(day.HourlyPrecipMm, num(at(hr.Precip, j), 0, 500))
			day.HourlyPrecipChance = append(day.HourlyPrecipChance, percent(at(hr.PrecipProb, j)))
		}
		st.Days = append(st.Days, day)
	}
	return st
}
