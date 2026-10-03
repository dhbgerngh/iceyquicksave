package agent

import (
	"errors"
	"reflect"
	"testing"
)

type fakeLoad struct {
	start  func() error
	closed bool
}

func (l *fakeLoad) Start() error         { return l.start() }
func (l *fakeLoad) Ready() (bool, error) { return true, nil }
func (l *fakeLoad) ApplyPlayer() error   { return nil }
func (l *fakeLoad) Close()               { l.closed = true }

func TestLoadPreparedWhilePausedAndStartedImmediatelyAfterResume(t *testing.T) {
	paused := true
	var events []string
	op := &fakeLoad{start: func() error {
		if paused {
			t.Fatal("load coroutine started while blocked")
		}
		events = append(events, "load")
		return nil
	}}
	got, e := prepareAndStartLoad(func() (loadOperation, error) {
		if !paused {
			t.Fatal("unpaused before file validation and decoding")
		}
		events = append(events, "prepare")
		return op, nil
	}, func() error { paused = false; events = append(events, "resume"); return nil })
	if e != nil || got != op || op.closed {
		t.Fatalf("unexpected result: %v, %v", got, e)
	}
	if !reflect.DeepEqual(events, []string{"prepare", "resume", "load"}) {
		t.Fatalf("wrong ordering: %v", events)
	}
}

func TestLoadFailureDoesNotStartWithInvalidDataOrFailedResume(t *testing.T) {
	for _, failure := range []string{"prepare", "resume", "start"} {
		t.Run(failure, func(t *testing.T) {
			bad := errors.New(failure)
			resumed, started := false, false
			op := &fakeLoad{start: func() error { started = true; return bad }}
			got, e := prepareAndStartLoad(func() (loadOperation, error) {
				if failure == "prepare" {
					return nil, bad
				}
				return op, nil
			}, func() error {
				resumed = true
				if failure == "resume" {
					return bad
				}
				return nil
			})
			if got != nil || !errors.Is(e, bad) {
				t.Fatalf("failure lost: %v", e)
			}
			if resumed != (failure != "prepare") || started != (failure == "start") || op.closed != (failure != "prepare") {
				t.Fatalf("unsafe failure cleanup: resume=%v start=%v closed=%v", resumed, started, op.closed)
			}
		})
	}
}
