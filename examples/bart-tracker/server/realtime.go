package server

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// liveTrip is one trip in BART's realtime feed: the stops it has still to
// make, in order, as GTFS platform ids, with predicted times.
type liveTrip struct {
	id    string
	stops []liveStop
}

type liveStop struct {
	platform string
	// arrive and leave are predictions; either may be zero if the feed
	// gives only the other.
	arrive, leave time.Time
}

// at is when the train is at the stop: its arrival, else its departure.
func (s liveStop) at() time.Time {
	if !s.arrive.IsZero() {
		return s.arrive
	}
	return s.leave
}

// Field numbers in gtfs-realtime.proto (https://gtfs.org/realtime/), for the
// few fields read here. Reading them directly keeps GTFS-realtime's large
// proto2 schema out of the widget's own.
const (
	feedHeader, feedEntity        = 1, 2
	headerTimestamp               = 3
	entityTripUpdate              = 3
	tripUpdateTrip, tripUpdateSTU = 1, 2
	tripID, tripRelationship      = 1, 4
	stuArrival, stuDeparture      = 2, 3
	stuStopID, stuRelationship    = 4, 5
	eventTime                     = 2

	tripCanceled = 3 // TripDescriptor.ScheduleRelationship.CANCELED
	stopSkipped  = 1 // StopTimeUpdate.ScheduleRelationship.SKIPPED
)

// parseFeed reads a GTFS-realtime FeedMessage: its time, and its trips that
// still run, with the stops they still make.
func parseFeed(b []byte) (time.Time, []liveTrip, error) {
	var at time.Time
	var trips []liveTrip
	err := eachField(b, func(num int, v []byte, n uint64) error {
		switch num {
		case feedHeader:
			return eachField(v, func(num int, _ []byte, n uint64) error {
				if num == headerTimestamp {
					at = time.Unix(int64(n), 0)
				}
				return nil
			})
		case feedEntity:
			return eachField(v, func(num int, v []byte, _ uint64) error {
				if num != entityTripUpdate {
					return nil
				}
				t, ok, err := parseTripUpdate(v)
				if ok {
					trips = append(trips, t)
				}
				return err
			})
		}
		return nil
	})
	if err != nil {
		return at, nil, fmt.Errorf("realtime feed: %w", err)
	}
	return at, trips, nil
}

func parseTripUpdate(b []byte) (liveTrip, bool, error) {
	var t liveTrip
	canceled := false
	err := eachField(b, func(num int, v []byte, _ uint64) error {
		switch num {
		case tripUpdateTrip:
			return eachField(v, func(num int, v []byte, n uint64) error {
				switch num {
				case tripID:
					t.id = string(v)
				case tripRelationship:
					canceled = n == tripCanceled
				}
				return nil
			})
		case tripUpdateSTU:
			var s liveStop
			skipped := false
			err := eachField(v, func(num int, v []byte, n uint64) error {
				switch num {
				case stuStopID:
					s.platform = string(v)
				case stuArrival:
					s.arrive = eventAt(v)
				case stuDeparture:
					s.leave = eventAt(v)
				case stuRelationship:
					skipped = n == stopSkipped
				}
				return nil
			})
			if err == nil && !skipped && s.platform != "" && !s.at().IsZero() {
				t.stops = append(t.stops, s)
			}
			return err
		}
		return nil
	})
	return t, err == nil && !canceled && t.id != "" && len(t.stops) > 0, err
}

// eventAt is a StopTimeEvent's time, or zero.
func eventAt(b []byte) time.Time {
	var at time.Time
	_ = eachField(b, func(num int, _ []byte, n uint64) error {
		if num == eventTime && n != 0 {
			at = time.Unix(int64(n), 0)
		}
		return nil
	})
	return at
}

// eachField calls f with each field of a protobuf message: its number, and
// its bytes (length-delimited) or its integer value (varint and fixed).
func eachField(b []byte, f func(num int, v []byte, n uint64) error) error {
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return protowire.ParseError(l)
		}
		b = b[l:]
		var v []byte
		var n uint64
		switch typ {
		case protowire.VarintType:
			n, l = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			v, l = protowire.ConsumeBytes(b)
		case protowire.Fixed32Type:
			var x uint32
			x, l = protowire.ConsumeFixed32(b)
			n = uint64(x)
		case protowire.Fixed64Type:
			n, l = protowire.ConsumeFixed64(b)
		default:
			l = protowire.ConsumeFieldValue(num, typ, b)
		}
		if l < 0 {
			return protowire.ParseError(l)
		}
		b = b[l:]
		if err := f(int(num), v, n); err != nil {
			return err
		}
	}
	return nil
}
