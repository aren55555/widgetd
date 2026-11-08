//go:build wasip1

// Command wasm is the test widget built for the sandbox. Its Config.label
// picks what it does, so one module covers every sandbox test:
//
//	basic   mount, snapshot, then deltas forever (checkpointing when asked)
//	kvput   store "k" in its KV; report found=1 in values
//	kvget   report in values whether "k" exists (1) or not (0)
//	fetch   fetch a host it didn't ask for; values = [1] if the policy said no
//	bad     emit a delta that breaks its rule; values = [1] if refused
//	alloc   allocate without bound
//	spin    loop forever without yielding
//	act     take actions; echo each tap into values
//	fail    return an error at once
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func main() { widget.Main(run, widget.WithChoices(choices), widget.WithVerify(verify)) }

// verify objects to the label "reject", tidies "tidy" to "TIDIED", and
// approves anything else as it is.
func verify(ctx context.Context, h widget.Host) (widget.Verdict, error) {
	var cfg testwidgetv1.Config
	if err := h.Config(&cfg); err != nil {
		return widget.Verdict{}, err
	}
	switch cfg.GetLabel() {
	case "reject":
		return widget.Verdict{Violations: []widget.Violation{{Field: "label", Message: "not that one"}}}, nil
	case "tidy":
		return widget.Verdict{Config: &testwidgetv1.Config{Label: "TIDIED"}}, nil
	}
	return widget.Verdict{}, nil
}

// choices echoes the unsaved label back (proving it reads the settings being
// edited) and reports whether mounting a card was refused, as it must be.
func choices(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
	var cfg testwidgetv1.Config
	if err := h.Config(&cfg); err != nil {
		return nil, err
	}
	_, mountErr := widget.MountState[*testwidgetv1.State](h, "nope", widget.MountOptions{})
	return map[string][]widget.Choice{
		"label": {{Value: cfg.GetLabel() + "!", Label: "echo"}, {Value: fmt.Sprint(mountErr != nil)}},
	}, nil
}

func run(ctx context.Context, h widget.Host) error {
	var cfg testwidgetv1.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	card, err := widget.Mount[*testwidgetv1.State, *testwidgetv1.Delta](h, "main", widget.MountOptions{Title: cfg.GetLabel()})
	if err != nil {
		return err
	}
	state := &testwidgetv1.State{Label: cfg.GetLabel()}
	report := func(v int64) error {
		state.Values = []int64{v}
		return card.Snapshot(state)
	}
	flag := func(ok bool) int64 {
		if ok {
			return 1
		}
		return 0
	}

	switch cfg.GetLabel() {
	case "basic":
		if err := card.Snapshot(state); err != nil {
			return err
		}
		for i := int64(1); ; i++ {
			state.Values = append(state.Values, i)
			if err := card.Delta(&testwidgetv1.Delta{Value: i}, func() *testwidgetv1.State { return state }); err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "kvput":
		if err := h.KV().Put("k", []byte("v")); err != nil {
			return err
		}
		_, ok, err := h.KV().Get("k")
		if err != nil {
			return err
		}
		if err := report(flag(ok)); err != nil {
			return err
		}
	case "kvget":
		_, ok, err := h.KV().Get("k")
		if err != nil {
			return err
		}
		if err := report(flag(ok)); err != nil {
			return err
		}
	case "fetch":
		req, _ := http.NewRequest(http.MethodGet, "https://not-requested.example.com/", nil)
		_, err := h.Fetch(req)
		var pe *widget.PolicyError
		if err := report(flag(errors.As(err, &pe))); err != nil {
			return err
		}
	case "bad":
		if err := card.Snapshot(state); err != nil {
			return err
		}
		err := card.Delta(&testwidgetv1.Delta{Value: -1}, func() *testwidgetv1.State { return state })
		if err := report(flag(err != nil)); err != nil {
			return err
		}
	case "alloc":
		var hog [][]byte
		for {
			hog = append(hog, make([]byte, 16<<20))
			hog[len(hog)-1][0] = 1
		}
	case "fail":
		return errors.New("deliberately failed")
	case "act":
		ac, err := widget.Mount[*testwidgetv1.State, *testwidgetv1.Delta](h, "act", widget.WithAction[*testwidgetv1.Action](widget.MountOptions{}))
		if err != nil {
			return err
		}
		if err := ac.Snapshot(state); err != nil {
			return err
		}
		for a := range widget.Actions[*testwidgetv1.Action](ac) {
			state.Values = []int64{a.GetTap()}
			if err := ac.Snapshot(state); err != nil {
				return err
			}
		}
	case "spin":
		if err := card.Snapshot(state); err != nil {
			return err
		}
		for x := 0; ; x++ {
			_ = x * x
		}
	}
	// A sandboxed widget runs until the host stops it. Blocking forever with
	// no timer would trip the Go runtime's deadlock check, so sleep.
	for {
		time.Sleep(time.Hour)
	}
}
