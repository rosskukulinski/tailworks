//go:build linux

package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/guest/proto"
)

// pool keeps pre-booted, warm microVMs that hold no tenant code (see
// proto.GuestConfig.Pool): a deploy or restart claims one and injects its
// workload, instead of booting a VM (Backend.Boot).
//
// Rules:
//   - single use: a claimed VM belongs to that workload until it is
//     destroyed; it is never returned to the pool, never reused for another
//     workload or tenant. A pooled VM that fails its claim is destroyed too.
//   - budget: the pool only refills while all of this backend's VMs
//     (pooled, claimed and cold-booted) plus the next one fit in the
//     huge-page reservation (`tw host setup --microvm`: vm.nr_hugepages,
//     default 4 VMs' worth, ≤ ¼ of RAM), and only if that many 2 MiB pages
//     are actually free right now (other users of huge pages, including
//     other hostds, count). Pool VMs are booted with huge pages "on": a
//     pool VM never falls back to 4 KiB pages.
//   - refill is in the background, one VM at a time, after every claim.
type pool struct {
	b         *Backend
	target    int
	budgetMiB int
	hugeOnly  bool
	ctx       context.Context
	cancel    context.CancelFunc
	kickCh    chan struct{}
	wg        sync.WaitGroup

	mu      sync.Mutex
	idle    []*Machine
	filling int
	st      PoolStats
	// budget accounting: every live VM of this backend (pooled or not)
	live map[*Machine]bool
	// for tests: free 2 MiB pages (default: sysfs)
	freePages func() int
}

// PoolStats is the warm pool's accounting (Backend.PoolStats).
type PoolStats struct {
	Target    int `json:"target"`
	BudgetMiB int `json:"budgetMiB"`
	VMMiB     int `json:"vmMiB"`
	Idle      int `json:"idle"`
	Filling   int `json:"filling"`
	Live      int `json:"live"`    // this backend's VMs now (pooled + claimed + cold)
	MaxLive   int `json:"maxLive"` // the most at any time
	Booted    int `json:"booted"`  // pool VMs booted
	Claims    int `json:"claims"`
	Misses    int `json:"misses"`       // Boot() with an empty pool (cold boot)
	Failed    int `json:"failed"`       // pool boots that failed
	DiedIdle  int `json:"diedIdle"`     // pooled VMs that exited before a claim
	SkipQuota int `json:"skippedQuota"` // refills skipped: budget or free huge pages
	Rotated   int `json:"rotated"`      // idle VMs discarded because their base was rotated out
}

// hugePagesTotal2M is vm.nr_hugepages for 2 MiB pages (the reservation).
func hugePagesTotal2M() int {
	b, err := os.ReadFile("/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// newPool sizes the pool from cfg (Pool: 0 = auto, < 0 = off, n = n VMs)
// and starts its refill loop. nil: no pool.
func newPool(b *Backend) *pool {
	cfg := b.cfg
	if cfg.Pool < 0 {
		return nil
	}
	p := &pool{b: b, kickCh: make(chan struct{}, 1), live: map[*Machine]bool{}, freePages: FreeHugePages2M}
	p.hugeOnly = cfg.HugePages != "off"
	p.budgetMiB = cfg.PoolBudgetMiB
	if p.budgetMiB == 0 && p.hugeOnly {
		p.budgetMiB = hugePagesTotal2M() * 2
	}
	vms := p.budgetMiB / cfg.MemMiB
	p.target = cfg.Pool
	if p.target == 0 { // auto: half the reservation, at most 2
		p.target = min(2, vms/2)
	}
	if p.target <= 0 || vms < 1 {
		cfg.Log.Info("microVM warm pool off", "pool", cfg.Pool, "budget_mib", p.budgetMiB, "vm_mib", cfg.MemMiB, "hugepages", cfg.HugePages)
		return nil
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.run()
	cfg.Log.Info("microVM warm pool", "target", p.target, "budget_mib", p.budgetMiB, "vm_mib", cfg.MemMiB)
	return p
}

func (p *pool) kick() {
	select {
	case p.kickCh <- struct{}{}:
	default:
	}
}

// wantMore: below target, and the next VM fits the budget and free pages.
func (p *pool) wantMore() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil || len(p.idle)+p.filling >= p.target {
		return false
	}
	need := p.b.cfg.MemMiB
	if (len(p.live)+p.filling+1)*need > p.budgetMiB || (p.hugeOnly && p.freePages()*2 < need) {
		p.st.SkipQuota++
		return false
	}
	return true
}

func (p *pool) run() {
	defer p.wg.Done()
	fails := 0
	for {
		for p.wantMore() {
			p.mu.Lock()
			p.filling++
			p.mu.Unlock()
			o := bootOpts{name: "pool", pool: true}
			if !p.b.cfg.SnapshotRefill {
				// boot (the default): the fastest claims
			} else if id := p.b.baseID(p.ctx, p.b.curBase() != nil); id != "" {
				o.restore = id // snapshots on: refill by restoring the base
			}
			m, err := p.b.boot(p.ctx, o)
			if err == nil && o.restore != "" {
				// Re-warm the restored copy in the background (its first
				// workerd would otherwise pay the restored VM's first-touch
				// cost at claim time).
				rctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
				if r, rerr := m.Op(rctx, proto.Msg{Op: "rewarm"}); rerr != nil {
					m.Stop()
					err = fmt.Errorf("rewarm: %w", rerr)
				} else {
					m.mu.Lock()
					m.info.Stages["rewarm"] = r.Stages["rewarm_ms"]
					m.mu.Unlock()
				}
				cancel()
			}
			p.mu.Lock()
			p.filling--
			if err != nil {
				p.st.Failed++
				p.mu.Unlock()
				if p.ctx.Err() == nil {
					p.b.cfg.Log.Warn("warm pool: boot failed", "err", err)
				}
				fails++
				break
			}
			fails = 0
			p.st.Booted++
			p.idle = append(p.idle, m)
			p.mu.Unlock()
			go func() {
				<-m.done
				p.mu.Lock()
				for i, x := range p.idle {
					if x == m {
						p.idle = append(p.idle[:i], p.idle[i+1:]...)
						p.st.DiedIdle++
						break
					}
				}
				p.mu.Unlock()
				p.kick()
			}()
		}
		wait := 10 * time.Second
		if fails > 0 {
			wait = time.Duration(min(fails, 6)) * 5 * time.Second
		}
		select {
		case <-p.kickCh:
			if fails > 0 { // don't spin on a broken boot
				select {
				case <-time.After(time.Second):
				case <-p.ctx.Done():
					return
				}
			}
		case <-time.After(wait):
		case <-p.ctx.Done():
			return
		}
	}
}

// roomForOne: one more VM of this backend fits the budget and the free
// huge pages now (the base VM's check; refills use wantMore).
func (p *pool) roomForOne() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	need := p.b.cfg.MemMiB
	return (len(p.live)+1)*need <= p.budgetMiB && (!p.hugeOnly || p.freePages()*2 >= need)
}

// dropBase destroys the idle pooled VMs restored from base id (rotated
// out); the pool refills from the current base.
func (p *pool) dropBase(id string) {
	p.mu.Lock()
	var drop, keep []*Machine
	for _, m := range p.idle {
		if m.info.Base == id {
			drop = append(drop, m)
		} else {
			keep = append(keep, m)
		}
	}
	p.idle = keep
	p.st.Rotated += len(drop)
	p.mu.Unlock()
	for _, m := range drop {
		m.Stop()
	}
	p.kick()
}

// take removes and returns the oldest live pooled VM (nil if none).
func (p *pool) take() *Machine {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.idle) > 0 {
		m := p.idle[0]
		p.idle = p.idle[1:]
		select {
		case <-m.done:
			continue
		default:
		}
		p.st.Claims++
		return m
	}
	p.st.Misses++
	return nil
}

// track counts a live VM of this backend against the budget until it exits.
func (p *pool) track(m *Machine) {
	p.mu.Lock()
	p.live[m] = true
	p.st.MaxLive = max(p.st.MaxLive, len(p.live))
	p.mu.Unlock()
	go func() {
		<-m.done
		p.mu.Lock()
		delete(p.live, m)
		p.mu.Unlock()
		p.kick()
	}()
}

func (p *pool) stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.st
	s.Target, s.BudgetMiB, s.VMMiB, s.Idle, s.Filling, s.Live = p.target, p.budgetMiB, p.b.cfg.MemMiB, len(p.idle), p.filling, len(p.live)
	return s
}

// close stops refilling and destroys every pooled VM.
func (p *pool) close() {
	p.cancel()
	p.wg.Wait()
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, m := range idle {
		m.Stop()
	}
}

// WaitPool waits until the warm pool holds at least n idle VMs.
func (b *Backend) WaitPool(ctx context.Context, n int) error {
	if b.pool == nil {
		return errors.New("no warm pool")
	}
	for {
		if b.pool.stats().Idle >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("warm pool: %d idle, want %d: %w", b.pool.stats().Idle, n, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
