package hostd

import (
	"sync"

	"github.com/rosskukulinski/tailworks/internal/srcmap"
)

// stackMaps are an instance's parsed source maps (bundle.App.SourceMaps),
// by module name ("dist/index.js"), parsed on first use. A map that fails
// to parse is remembered as nil: its frames stay as workerd reported them.
type stackMaps struct {
	mu   sync.Mutex
	maps map[string]*srcmap.Map
}

// mapStack maps an uncaught exception's stack from the bundle back to the
// app's sources (issue #42): `dist/index.js:129:54` becomes
// `src/index.ts:5:11`. Frames in modules without a map are unchanged.
func (inst *instance) mapStack(stack string) string {
	if inst == nil || inst.app == nil || len(inst.app.SourceMaps) == 0 || stack == "" {
		return stack
	}
	sm := &inst.stackMaps
	return srcmap.MapStack(stack, func(mod string) *srcmap.Map {
		raw, ok := inst.app.SourceMaps[mod+".map"]
		if !ok {
			return nil
		}
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if m, ok := sm.maps[mod]; ok {
			return m
		}
		m, err := srcmap.Parse(raw)
		if err != nil {
			inst.h.log.Warn("source map", "workload", inst.w.name, "module", mod, "err", err)
			m = nil
		}
		if sm.maps == nil {
			sm.maps = map[string]*srcmap.Map{}
		}
		sm.maps[mod] = m
		return m
	})
}
