//go:build e2ecompat

package compat

import "fmt"

// deployCompanions prepares and deploys p's Companions (the Workers its
// service bindings name), each under its own ID, before p is deployed.
func deployCompanions(p *Project, tw twFunc) error {
	for _, c := range p.Companions {
		pr, err := Prepare(c, CompatTag)
		if err != nil {
			return fmt.Errorf("prepare companion %s: %v", c.ID, err)
		}
		if _, se, _, err := tw("", "", "deploy", pr.DeployDir, "--json", "--wait", "4m"); err != nil {
			return fmt.Errorf("tw deploy companion %s: %v\n%s", c.ID, err, tail(se, 3000))
		}
	}
	return nil
}
