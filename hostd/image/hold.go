package image

import (
	"context"
	"fmt"
	"os"

	"github.com/rosskukulinski/tailworks/internal/container"
)

// heldTries bounds how often GetHeld converts again when a concurrent GC
// removed the image between its conversion and the hold.
const heldTries = 3

// GetHeld returns ref converted for tenant, already held (see Hold): the
// caller boots from img.Path and calls release when the container is gone.
// The hold is taken before GC runs, so GC never removes the image it was
// run for, even when that one image is larger than MaxBytes.
func (s *Store) GetHeld(ctx context.Context, tenant, ref string) (img *Image, release func(), err error) {
	return s.getAndHold(func() (*Image, error) { return s.get(ctx, tenant, ref) })
}

// GetPinnedHeld is GetHeld for an image pinned at deploy (GetPinned).
func (s *Store) GetPinnedHeld(ctx context.Context, tenant string, p container.Pin) (img *Image, release func(), err error) {
	return s.getAndHold(func() (*Image, error) { return s.getPinned(ctx, tenant, p) })
}

// Get returns ref converted for tenant (from the cache, or converted now).
// Nothing holds it: a later GC may remove it (GetHeld to boot from it).
func (s *Store) Get(ctx context.Context, tenant, ref string) (*Image, error) {
	img, release, err := s.GetHeld(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	release()
	return img, nil
}

// GetPinned returns the image of pin for tenant: from the cache, or pulled
// from the registry by digest and converted now. Nothing holds it
// (GetPinnedHeld to boot from it).
func (s *Store) GetPinned(ctx context.Context, tenant string, p container.Pin) (*Image, error) {
	img, release, err := s.GetPinnedHeld(ctx, tenant, p)
	if err != nil {
		return nil, err
	}
	release()
	return img, nil
}

// getAndHold runs get, holds the image it returns and only then runs GC. Hold and
// GC's removals both run under s.mu, so once the hold is in place GC skips
// the image; if a GC removed it first (another start's, between get and the
// hold), the file is gone and get runs again.
func (s *Store) getAndHold(get func() (*Image, error)) (*Image, func(), error) {
	for range heldTries {
		img, err := get()
		if err != nil {
			return nil, nil, err
		}
		release := s.Hold(img.Path)
		if _, err := os.Stat(img.Path); err != nil {
			release()
			continue
		}
		s.gc()
		return img, release, nil
	}
	return nil, nil, fmt.Errorf("image: evicted from the cache %d times while starting; raise -container-image-cache-max-mib", heldTries)
}
