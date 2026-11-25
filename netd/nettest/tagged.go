package nettest

import (
	"context"
	"fmt"
	"time"

	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// NewTaggedNode is NewNode for a node that advertises tags. testcontrol
// grants advertised tags (TagOwners is non-nil, see StartControl), so peers
// see a tagged node with no user. No tags: same as NewNode.
func NewTaggedNode(ctx context.Context, controlURL, key, hostname, dir string, tags []string) (*Node, error) {
	if controlURL == "" {
		return nil, fmt.Errorf("nettest: controlURL required")
	}
	logf := func(string, ...any) {}
	s := &tsnet.Server{
		Hostname: hostname, ControlURL: controlURL, AuthKey: key, Ephemeral: true,
		Store: new(mem.Store), Dir: dir, AdvertiseTags: tags,
		Logf: logf, UserLogf: logf,
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := s.Up(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("up %s: %w", hostname, err)
	}
	lc, err := s.LocalClient()
	if err != nil {
		s.Close()
		return nil, err
	}
	lc.OmitAuth = true
	ip4, _ := s.TailscaleIPs()
	return &Node{Server: s, LC: lc, IP4: ip4.String()}, nil
}
