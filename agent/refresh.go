package agent

// Refresh (issue #490): after tw upgrade, the new tw rewrites the skills an
// older tw wrote, so they match the new release. It only touches content
// that carries tailworks' marker: it never creates a skill file or block
// where there was none (the user didn't set that agent up, or removed it),
// and never reports files tailworks didn't write.

// refreshOnly drops the changes a refresh doesn't make: Create and Skip.
// Without opts.Refresh it returns cs unchanged.
func refreshOnly(cs []Change, opts Options) []Change {
	if !opts.Refresh {
		return cs
	}
	out := []Change{}
	for _, c := range cs {
		if c.Action != Create && c.Action != Skip {
			out = append(out, c)
		}
	}
	return out
}

// Stamped reports whether the plan found content tailworks wrote: with
// Refresh, any change left (update, unchanged, remove) is tw-marked.
func (p *Plan) Stamped() bool { return len(p.Changes) > 0 }
