package server

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"buf.build/go/protovalidate"

	pb "github.com/aren55555/widgetd/examples/weather/gen/weather/v1"
)

// The fixture is Berlin, 24–28 Oct 2026: clocks go back on the 25th.
var now = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func fixture(t *testing.T) *forecast {
	t.Helper()
	b, err := os.ReadFile("testdata/forecast.json")
	if err != nil {
		t.Fatal(err)
	}
	var f forecast
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

func valid(t *testing.T, st *pb.State) {
	t.Helper()
	if err := protovalidate.Validate(st); err != nil {
		t.Fatalf("state breaks its rules: %v", err)
	}
}

func TestCondition(t *testing.T) {
	for code, want := range map[int]pb.Condition{
		0: pb.Condition_CONDITION_CLEAR, 1: pb.Condition_CONDITION_CLEAR,
		2: pb.Condition_CONDITION_PARTLY_CLOUDY, 3: pb.Condition_CONDITION_CLOUDY,
		45: pb.Condition_CONDITION_FOG, 55: pb.Condition_CONDITION_DRIZZLE,
		63: pb.Condition_CONDITION_RAIN, 81: pb.Condition_CONDITION_RAIN,
		75: pb.Condition_CONDITION_SNOW, 86: pb.Condition_CONDITION_SNOW,
		95: pb.Condition_CONDITION_THUNDERSTORM, 42: pb.Condition_CONDITION_UNSPECIFIED,
	} {
		if got := condition(&code); got != want {
			t.Errorf("code %d: %v, want %v", code, got, want)
		}
	}
	if got := condition(nil); got != pb.Condition_CONDITION_UNSPECIFIED {
		t.Errorf("nil: %v", got)
	}
}

func TestBuild(t *testing.T) {
	aqi := 37.6
	st := build(now, "Berlin", pb.Units_UNITS_CELSIUS, fixture(t), &aqi)
	valid(t, st)

	if st.GetName() != "Berlin" || st.GetTimeZone() != "Europe/Berlin" || st.GetTemperature() != pb.Units_UNITS_CELSIUS {
		t.Fatalf("header: %q %q %v", st.GetName(), st.GetTimeZone(), st.GetTemperature())
	}
	if c := st.GetCurrent(); c.GetTemperatureC() != 12.4 || c.GetCondition() != pb.Condition_CONDITION_CLEAR || !c.GetIsDay() || c.GetUsAqi() != 38 {
		t.Fatalf("current: %v", c)
	}
	// Four days, though five were read: the fifth only marks the fourth's end.
	if n := len(st.GetDays()); n != 4 {
		t.Fatalf("%d days, want 4", n)
	}
	for i, d := range st.GetDays() {
		if i > 0 && d.GetStart() != st.GetDays()[i-1].GetEnd() {
			t.Errorf("day %d starts at %d, not where day %d ends", i, d.GetStart(), i-1)
		}
		hours := int((d.GetEnd() - d.GetStart()) / 3600)
		if len(d.GetHourlyPrecipMm()) != hours || len(d.GetHourlyPrecipChance()) != hours {
			t.Errorf("day %d: %d/%d hours, want %d", i, len(d.GetHourlyPrecipMm()), len(d.GetHourlyPrecipChance()), hours)
		}
	}
	// The 25th is the clock change: 25 hours.
	if got := st.GetDays()[1].GetEnd() - st.GetDays()[1].GetStart(); got != 25*3600 {
		t.Errorf("the 25th is %d s long, want 25 h", got)
	}

	rainy := st.GetDays()[2]
	if rainy.GetCondition() != pb.Condition_CONDITION_RAIN || rainy.GetPrecipChance() != 95 || rainy.GetHumidity() != 92 {
		t.Errorf("rainy day: %v", rainy)
	}
	var sum float64
	for _, mm := range rainy.GetHourlyPrecipMm() {
		sum += float64(mm)
	}
	if math.Abs(sum-6.1) > 0.01 || rainy.GetHourlyPrecipChance()[9] != 95 {
		t.Errorf("rainy day's hours: sum %.2f, 09:00 chance %d", sum, rainy.GetHourlyPrecipChance()[9])
	}
	// 359.6° rounds to north, not to an out-of-range 360.
	if rainy.GetWindFromDeg() != 0 {
		t.Errorf("wind from %d°, want 0", rainy.GetWindFromDeg())
	}
}

func TestBuildWithGaps(t *testing.T) {
	f := fixture(t)
	f.Current.Temperature = nil
	f.Daily.High = f.Daily.High[:1] // a short array
	nan := math.NaN()
	f.Daily.Low[0] = &nan
	st := build(now, "Berlin", pb.Units_UNITS_SCREEN_SETTING, f, nil)
	valid(t, st)
	if st.GetCurrent().UsAqi != nil {
		t.Error("an AQI with none read")
	}
	if st.GetDays()[1].GetHighC() != 0 || st.GetDays()[0].GetLowC() != 0 {
		t.Error("gaps should read as zero")
	}
	// The last day has no next day to end at: it ends 24 h on.
	f.Daily.Time = f.Daily.Time[:4]
	st = build(now, "Berlin", pb.Units_UNITS_SCREEN_SETTING, f, nil)
	if d := st.GetDays()[3]; d.GetEnd()-d.GetStart() != 24*3600 {
		t.Errorf("last day is %d s long", d.GetEnd()-d.GetStart())
	}
}

func TestLabel(t *testing.T) {
	for _, tc := range []struct {
		p    place
		want string
	}{
		{place{Name: "Springfield", Admin1: "Illinois", Country: "United States"}, "Springfield, Illinois, United States"},
		{place{Name: "Singapore", Admin1: "", Country: "Singapore"}, "Singapore"},
		{place{Name: "Berlin", Admin1: "Berlin", Country: "Germany"}, "Berlin, Germany"},
	} {
		if got := tc.p.label(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.p, got, tc.want)
		}
	}
}
