//go:build e2edataplane

package dataplane

import (
	"strings"
	"testing"
	"time"
)

// TestCronCheckers plants one violation of each cron and alarm invariant
// in an otherwise clean synthetic history and checks that the right
// checker, and only it, fails. No cluster: go test -tags e2edataplane
// -run TestCronCheckers ./e2e/dataplane/
func TestCronCheckers(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	min := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	// clean: cron "* * * * *" on h1 for minutes 1..8, each run reporting
	// start and end; an alarm chain firing every 2 s.
	clean := func() *cronHist {
		ch := &cronHist{h: &history{}}
		ch.win = cronWindow{Start: t0.Add(5 * time.Second), Healed: min(8), End: min(9).Add(30 * time.Second)}
		ch.scheds = []schedEv{{WL: cronSL, Crons: []string{"* * * * *"}, Sent: t0, Active: t0.Add(2 * time.Second)},
			{WL: cronSF, Crons: []string{}, Sent: t0, Active: t0.Add(2 * time.Second)}}
		ch.deps = []wlDep{{WL: cronSL, Host: "h1", Epoch: 1, At: t0.Add(2 * time.Second)}, {WL: cronSF, Host: "h1", Epoch: 1, At: t0.Add(2 * time.Second)}}
		for i := 1; i <= 9; i++ {
			id := "r" + min(i).Format("1504")
			ch.runs = append(ch.runs,
				cronEv{WL: cronSL, Cron: "* * * * *", Run: id, Phase: "start", St: min(i).UnixMilli(), Epoch: 1, Node: "h1-cron", At: min(i).Add(200 * time.Millisecond)},
				cronEv{WL: cronSL, Cron: "* * * * *", Run: id, Phase: "end", St: min(i).UnixMilli(), Epoch: 1, Node: "h1-cron", At: min(i).Add(300 * time.Millisecond)})
		}
		for s, at := 1, t0.Add(10*time.Second); at.Before(ch.win.End.Add(-5 * time.Second)); s, at = s+1, at.Add(2*time.Second) {
			ch.alarms = append(ch.alarms,
				alarmEv{Cls: "alm", Name: "a0", Chain: "c1", Kind: "fire", Seq: s, Node: "h1-cronobj", At: at},
				alarmEv{Cls: "alm", Name: "a0", Chain: "c1", Kind: "set", Seq: s + 1, Delay: 2000, Node: "h1-cronobj", At: at.Add(10 * time.Millisecond)},
				alarmEv{Cls: "sch", Name: "s0", Chain: "every", Kind: "fire", Seq: s, Node: "h1-s0", At: at})
		}
		return ch
	}
	cases := []struct {
		name, want string // want: the failing checker's ID ("" = all pass)
		plant      func(ch *cronHist)
	}{
		{"clean", "", func(*cronHist) {}},
		{"duplicate run", "C1", func(ch *cronHist) {
			ch.runs = append(ch.runs, cronEv{WL: cronSL, Cron: "* * * * *", Run: "dup", Phase: "start", St: min(4).UnixMilli(), Node: "h1-cron", At: min(4).Add(time.Second)})
		}},
		{"rerun under a clock step, no restart", "C1", func(ch *cronHist) {
			ch.h.faults = []faultEv{{Kind: "clock-back", Host: "h1", Start: min(4).Add(10 * time.Second), End: min(6), Detail: "clock -2m0s for 1m30s"}}
			ch.runs = append(ch.runs, cronEv{WL: cronSL, Cron: "* * * * *", Run: "again", Phase: "start", St: min(4).UnixMilli(), Node: "h1-cron", At: min(5).Add(30 * time.Second)})
		}},
		{"rerun after a restart under a clock step (#594, fixed)", "C1", func(ch *cronHist) {
			ch.h.faults = []faultEv{{Kind: "clock-back", Host: "h1", Start: min(4).Add(10 * time.Second), End: min(6), Detail: "clock -2m0s for 1m30s, hostd on h1 restarted half way"}}
			ch.runs = append(ch.runs, cronEv{WL: cronSL, Cron: "* * * * *", Run: "again", Phase: "start", St: min(4).UnixMilli(), Node: "h1-cron", At: min(5).Add(30 * time.Second)})
		}},
		{"rerun after a restart, no clock step", "C1", func(ch *cronHist) {
			ch.h.faults = []faultEv{{Kind: "kill-host", Host: "h1", Start: min(4).Add(10 * time.Second), End: min(4).Add(15 * time.Second)}}
			ch.runs = append(ch.runs, cronEv{WL: cronSL, Cron: "* * * * *", Run: "again", Phase: "start", St: min(4).UnixMilli(), Node: "h1-cron", At: min(4).Add(20 * time.Second)})
		}},
		{"overlap", "C2", func(ch *cronHist) {
			for i := range ch.runs {
				if ch.runs[i].Run == "r1003" && ch.runs[i].Phase == "end" {
					ch.runs[i].At = min(4).Add(10 * time.Second) // run 3 still going after run 4 started
				}
			}
		}},
		{"stateless overlap after a takeover (#566)", "", func(ch *cronHist) {
			ch.deps = append(ch.deps, wlDep{WL: cronSL, Host: "h2", Epoch: 2, At: min(4).Add(-5 * time.Second)})
			ch.h.faults = []faultEv{{Kind: "cut-takeover", Host: "h1", Start: min(3).Add(5 * time.Second), End: min(4).Add(2 * time.Second)}}
			for i := range ch.runs {
				if ch.runs[i].St >= min(4).UnixMilli() {
					ch.runs[i].Node = "h2-cron"
				}
				if ch.runs[i].Run == "r1003" && ch.runs[i].Phase == "end" {
					ch.runs[i].At = min(4).Add(10 * time.Second)
				}
			}
		}},
		{"skipped minute", "C3", func(ch *cronHist) {
			var keep []cronEv
			for _, e := range ch.runs {
				if e.Run != "r1005" {
					keep = append(keep, e)
				}
			}
			ch.runs = keep
		}},
		{"skipped minute in a fault", "", func(ch *cronHist) {
			var keep []cronEv
			for _, e := range ch.runs {
				if e.Run != "r1005" {
					keep = append(keep, e)
				}
			}
			ch.runs = keep
			ch.h.faults = []faultEv{{Kind: "kill-host", Host: "h1", Start: min(5).Add(-3 * time.Second), End: min(5).Add(2 * time.Second)}}
		}},
		{"run past the fence", "C3", func(ch *cronHist) {
			ch.h.faults = []faultEv{{Kind: "cut-short", Host: "h1", Start: min(5).Add(-40 * time.Second), End: min(5).Add(10 * time.Second)}}
		}},
		{"run on the old host", "C3", func(ch *cronHist) {
			ch.deps = append(ch.deps, wlDep{WL: cronSL, Host: "h2", Epoch: 2, At: min(6).Add(-20 * time.Second)})
			for i := range ch.runs {
				if ch.runs[i].St >= min(6).UnixMilli() && ch.runs[i].Run != "r1007" {
					ch.runs[i].Node = "h2-cron"
				}
			}
		}},
		{"old schedule after the change", "C4", func(ch *cronHist) {
			ch.scheds = append(ch.scheds, schedEv{WL: cronSL, Crons: []string{"*/2 * * * *"}, Sent: min(5).Add(10 * time.Second), Active: min(5).Add(15 * time.Second)})
			var keep []cronEv
			for _, e := range ch.runs {
				st := time.UnixMilli(e.St)
				switch {
				case st.Before(min(6)):
					keep = append(keep, e)
				case st.Minute()%2 == 0:
					e.Cron = "*/2 * * * *"
					keep = append(keep, e)
				}
			}
			ch.runs = append(keep, cronEv{WL: cronSL, Cron: "* * * * *", Run: "late", Phase: "start", St: min(7).UnixMilli(), Node: "h1-cron", At: min(7).Add(time.Second)})
		}},
		{"new schedule late", "C3", func(ch *cronHist) { // C4 too: checked below
			ch.scheds = append(ch.scheds, schedEv{WL: cronSL, Crons: []string{"* * * * *", "*/2 * * * *"}, Sent: min(5).Add(10 * time.Second), Active: min(5).Add(15 * time.Second)})
		}},
		{"stale alarm", "A1", func(ch *cronHist) {
			ch.alarms = append(ch.alarms, alarmEv{Cls: "falm", Name: "f0", Chain: "c9", Kind: "fire", Seq: 1, Node: "h2-f0", At: min(3)})
		}},
		{"lost alarm", "A1", func(ch *cronHist) {
			var keep []alarmEv
			for _, a := range ch.alarms {
				if !(a.Cls == "alm" && a.Kind == "fire" && a.Seq == 40) {
					keep = append(keep, a)
				}
			}
			ch.alarms = keep
		}},
		{"lost alarm in a move", "", func(ch *cronHist) {
			// The workload moves right after seq 40 is set, so the alarm
			// never fires (storage lost, documented), nor does the chain go on.
			var moved time.Time
			for _, a := range ch.alarms {
				if a.Cls == "alm" && a.Kind == "set" && a.Seq == 40 {
					moved = a.At.Add(time.Second)
				}
			}
			ch.deps = append(ch.deps, wlDep{WL: cronSF, Host: "h2", Epoch: 2, At: moved})
			ch.h.faults = []faultEv{{Kind: "move", Host: "h1", Start: moved.Add(-2 * time.Second), End: moved}}
			var keep []alarmEv
			for _, a := range ch.alarms {
				switch {
				case a.Cls == "alm" && a.Kind == "fire" && a.Seq >= 40, a.Cls == "alm" && a.Kind == "set" && a.Seq > 40:
				case a.Cls == "sch" && a.At.After(moved):
					a.Node = "h2-s0" // the agent restarted on h2
					keep = append(keep, a)
				default:
					keep = append(keep, a)
				}
			}
			ch.alarms = keep
		}},
		{"scheduleEvery gap", "A1", func(ch *cronHist) {
			var keep []alarmEv
			for _, a := range ch.alarms {
				if !(a.Cls == "sch" && a.At.After(min(4)) && a.At.Before(min(4).Add(30*time.Second))) {
					keep = append(keep, a)
				}
			}
			ch.alarms = keep
		}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			ch := clean()
			tc.plant(ch)
			for _, r := range checkCron(ch) {
				if r.Pass == (r.ID == tc.want) && !(tc.name == "new schedule late" && r.ID == "C4") {
					t.Errorf("%s: pass=%v (%s) %v", r.ID, r.Pass, r.Summary, r.Evidence)
				}
				if tc.name == "new schedule late" && r.ID == "C4" && r.Pass {
					t.Errorf("C4 passed a new trigger that never ran: %s", r.Summary)
				}
			}
		})
	}
}
