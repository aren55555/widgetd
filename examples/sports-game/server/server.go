// Package server is the sports-game widget's server half: one followed
// team. It reads the team's schedule, and for each game whose window is
// open (a few hours before the start until a day after the end) it shows a
// card and keeps its score current. Once a game starts, the card also carries
// Kalshi's market on who wins, which the view draws behind the score. Cards
// come and go by themselves; between games the instance shows nothing.
package server

import (
	"context"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	scheduleEvery = 30 * time.Minute
	livePoll      = 20 * time.Second
	idlePoll      = 5 * time.Minute
	// soon is how close to the start a game is polled like a live one, so
	// the first goal isn't five minutes late.
	soon = 15 * time.Minute
)

// window decides whether a game's card should be up at now, and until when.
// Every time is a Duration since the epoch (see at); end is 0 if unknown. A game not known to have ended (it
// may be running long, or delayed) stays up until twice its usual length
// has passed, plus the linger.
func window(start, end time.Duration, completed bool, before, after, typical, now time.Duration) (show bool, until time.Duration) {
	if now < start-before {
		return false, 0
	}
	switch {
	case end != 0:
		until = end + after
	case completed:
		until = start + typical + after
	default:
		until = start + 2*typical + after
	}
	return now < until, until
}

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	g := &games{
		h:       h,
		espn:    espn{h: h, league: cfg.GetLeague()},
		kalshi:  &kalshi{h: h, league: cfg.GetLeague(), games: map[string]*market{}},
		team:    strings.ToUpper(cfg.GetTeam()),
		before:  hours(cfg.GetHoursBefore(), 3),
		after:   hours(cfg.GetHoursAfter(), 24),
		typical: typicalLength[cfg.GetLeague()],
		cards:   map[string]*widget.StateCard[*pb.State]{},
		last:    map[string]*pb.State{},
		until:   map[string]time.Time{},
		sawLive: map[string]bool{},
	}
	return g.run(ctx)
}

func hours(n, def uint32) time.Duration {
	if n == 0 {
		n = def
	}
	return time.Duration(n) * time.Hour
}

type games struct {
	h       widget.Host
	espn    espn
	kalshi  *kalshi
	team    string
	before  time.Duration
	after   time.Duration
	typical time.Duration

	schedule    []scheduled
	scheduledAt time.Time
	cards       map[string]*widget.StateCard[*pb.State]
	last        map[string]*pb.State
	until       map[string]time.Time
	sawLive     map[string]bool
}

func (g *games) run(ctx context.Context) error {
	log := g.h.Logger()
	for {
		if g.schedule == nil || time.Since(g.scheduledAt) > scheduleEvery {
			_, s, err := g.espn.schedule(ctx, g.team)
			switch {
			case err != nil && g.schedule == nil:
				// Nothing to go on (an unknown team, say): fail, so screens
				// show why, and the host retries with backoff.
				return err
			case err != nil:
				log.Warn("sports-game: schedule refresh failed; keeping the last one", "err", err)
			default:
				g.schedule, g.scheduledAt = s, time.Now()
			}
		}
		next := g.tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(next):
		}
	}
}

// tick brings every card up to date and says how long to wait for the next.
func (g *games) tick(ctx context.Context) time.Duration {
	log := g.h.Logger()
	now := time.Now()
	wait := idlePoll
	open := map[string]bool{}

	for _, s := range g.schedule {
		end := g.end(s.id)
		show, until := window(at(s.start), at(end), s.completed, g.before, g.after, g.typical, at(now))
		if !show {
			// Wake in time for the next game to open.
			if d := s.start.Add(-g.before).Sub(now); d > 0 && d < wait {
				wait = d
			}
			continue
		}
		open[s.id] = true
		st, err := g.espn.game(ctx, s.id)
		if err != nil {
			log.Warn("sports-game: game update failed", "game", s.id, "err", err)
			continue
		}
		st.Mine = g.team
		st.Market = g.kalshi.market(ctx, st, now)
		switch st.GetPhase() {
		case pb.State_PHASE_LIVE:
			g.sawLive[s.id] = true
			wait = min(wait, livePoll)
		case pb.State_PHASE_PRE:
			if time.UnixMilli(st.GetStartUnixMs()).Sub(now) < soon {
				wait = min(wait, livePoll)
			}
		case pb.State_PHASE_FINAL:
			if end.IsZero() {
				// Seen live and now final: it ended about now. Otherwise we
				// weren't watching, so assume it ran its usual length.
				end = time.UnixMilli(st.GetStartUnixMs()).Add(g.typical)
				if g.sawLive[s.id] {
					end = now
				}
				g.setEnd(s.id, end)
				_, until = window(at(s.start), at(end), true, g.before, g.after, g.typical, at(now))
			}
		case pb.State_PHASE_UNSPECIFIED:
			// ESPN said nothing we understand: keep the usual pace.
		}
		if err := g.show(s.id, st, time.Unix(0, int64(until))); err != nil {
			log.Warn("sports-game: card update failed", "game", s.id, "err", err)
		}
	}

	for id := range g.cards {
		if !open[id] {
			_ = g.h.Unmount("game-" + id) // the host may already have expired it
			delete(g.cards, id)
			delete(g.last, id)
			delete(g.until, id)
			delete(g.kalshi.games, id)
		}
	}
	return max(wait, time.Second)
}

// show mounts or updates a game's card. A changed window remounts it (the
// host keeps its data); an unchanged state isn't resent.
func (g *games) show(id string, st *pb.State, until time.Time) error {
	card, ok := g.cards[id]
	if !ok || !g.until[id].Equal(until) {
		// No title: the board then draws no heading, and the card (with the
		// market chart behind it) fills the whole tile. The teams are on it.
		c, err := widget.MountState[*pb.State](g.h, "game-"+id, widget.MountOptions{
			Layout:       widget.Layout{Cols: 2, Rows: 1},
			VisibleUntil: until,
		})
		if err != nil {
			return err
		}
		card = c
		g.cards[id], g.until[id] = c, until
		delete(g.last, id) // a remounted card gets its state again
	}
	if proto.Equal(st, g.last[id]) {
		return nil
	}
	if err := card.Snapshot(st); err != nil {
		return err
	}
	g.last[id] = st
	return nil
}

func (g *games) end(id string) time.Time {
	b, ok, err := g.h.KV().Get("end:" + id)
	if err != nil || !ok {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (g *games) setEnd(id string, t time.Time) {
	if err := g.h.KV().Put("end:"+id, []byte(strconv.FormatInt(t.UnixMilli(), 10))); err != nil {
		g.h.Logger().Warn("sports-game: couldn't save when a game ended", "game", id, "err", err)
	}
}

// at is a time as a Duration since the epoch (0 for the zero time), which
// keeps window's arithmetic plain.
func at(t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	return time.Duration(t.UnixNano())
}
