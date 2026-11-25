package publicspec

import (
	"strings"
	"testing"
)

func TestConnLimits(t *testing.T) {
	e := (&Limits{MaxConnsPerIP: 4}).Effective()
	if e.MaxConnsPerIP != 4 || e.UpgradeLifetimeSeconds != Defaults.UpgradeLifetimeSeconds {
		t.Errorf("effective %+v", e)
	}
	for _, c := range []struct {
		l    Limits
		want string
	}{
		{Limits{MaxConnsPerIP: Ceilings.MaxConnsPerIP + 1}, "maxConnsPerIp"},
		{Limits{UpgradeLifetimeSeconds: -1}, "upgradeLifetimeSeconds"},
		{Limits{UpgradeLifetimeSeconds: Ceilings.UpgradeLifetimeSeconds + 1}, "upgradeLifetimeSeconds"},
	} {
		s := &Spec{Routes: []string{"/*"}, Limits: &c.l}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.l, err)
		}
	}
}
