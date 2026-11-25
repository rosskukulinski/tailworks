package image

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

func exists(t *testing.T, img *Image) bool {
	t.Helper()
	_, err := os.Stat(img.Path)
	return err == nil
}

// pushN pushes n distinct images into tna/app and returns their pins' digests.
func (e *pinEnv) pushN(t *testing.T, n int) []string {
	t.Helper()
	var ds []string
	for i := range n {
		ds = append(ds, e.push(t, "tna/app", runtime.GOARCH, strings.Repeat(string(rune('a'+i)), 16000)))
	}
	return ds
}

// TestCacheCapHonoured: starts that release their images keep the cache
// within MaxBytes, dropping the least recently used.
func TestCacheCapHonoured(t *testing.T) {
	ctx := context.Background()
	e := newPinEnv(t)
	ds := e.pushN(t, 3)
	var imgs []*Image
	for _, d := range ds {
		img, release, err := e.store.GetPinnedHeld(ctx, "tna", pin("tna/app", d))
		if err != nil {
			t.Fatal(err)
		}
		release() // the container is gone
		imgs = append(imgs, img)
		if len(imgs) == 1 {
			e.store.MaxBytes = img.SizeB*2 + img.SizeB/2 // room for two
		}
	}
	if exists(t, imgs[0]) || !exists(t, imgs[1]) || !exists(t, imgs[2]) {
		t.Fatalf("want only the oldest evicted: %v %v %v", exists(t, imgs[0]), exists(t, imgs[1]), exists(t, imgs[2]))
	}
	if got := e.store.CacheBytes(); got > e.store.MaxBytes {
		t.Fatalf("cache %d > cap %d", got, e.store.MaxBytes)
	}
}

// TestCacheCapBelowOneImage: with a cap smaller than one image, the image a
// start just produced is never evicted by the GC that start runs; the next
// start's GC removes it once it is no longer held.
func TestCacheCapBelowOneImage(t *testing.T) {
	ctx := context.Background()
	e := newPinEnv(t)
	e.store.MaxBytes = 1
	ds := e.pushN(t, 2)
	a, releaseA, err := e.store.GetPinnedHeld(ctx, "tna", pin("tna/app", ds[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, a) {
		t.Fatal("GC evicted the image it was run for")
	}
	e.store.gc() // another start's GC while a runs
	if !exists(t, a) {
		t.Fatal("GC evicted an image in use")
	}
	releaseA()
	b, releaseB, err := e.store.GetPinnedHeld(ctx, "tna", pin("tna/app", ds[1]))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	if exists(t, a) || !exists(t, b) {
		t.Fatalf("want a evicted and b kept: a %v b %v", exists(t, a), exists(t, b))
	}
	// Unheld Get also keeps the image it returns through its own GC.
	e.store.MaxBytes = 1
	if img, err := e.store.GetPinned(ctx, "tna", pin("tna/app", ds[0])); err != nil || !exists(t, img) {
		t.Fatalf("GetPinned: %v (exists after its own GC: %v)", err, err == nil && exists(t, img))
	}
}

// TestCacheHeldPlusNewAboveCap: an image in use plus the one a start just
// produced may together exceed the cap; neither is evicted, and an unheld
// image is.
func TestCacheHeldPlusNewAboveCap(t *testing.T) {
	ctx := context.Background()
	e := newPinEnv(t)
	ds := e.pushN(t, 3)
	idle, err := e.store.GetPinned(ctx, "tna", pin("tna/app", ds[0]))
	if err != nil {
		t.Fatal(err)
	}
	run, releaseRun, err := e.store.GetPinnedHeld(ctx, "tna", pin("tna/app", ds[1]))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRun()
	e.store.MaxBytes = run.SizeB + run.SizeB/2 // room for one
	fresh, releaseFresh, err := e.store.GetPinnedHeld(ctx, "tna", pin("tna/app", ds[2]))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFresh()
	if exists(t, idle) || !exists(t, run) || !exists(t, fresh) {
		t.Fatalf("want idle evicted, running and fresh kept: %v %v %v", exists(t, idle), exists(t, run), exists(t, fresh))
	}
	if got := e.store.CacheBytes(); got <= e.store.MaxBytes {
		t.Fatalf("cache %d within cap %d: expected the two held images above it", got, e.store.MaxBytes)
	}
}

// TestHeldRetriesAfterEviction: when another start's GC removes the image
// between its conversion and the hold, the start converts it again rather than
// returning a path that is gone (getAndHold); it gives up after heldTries.
func TestHeldRetriesAfterEviction(t *testing.T) {
	ctx := context.Background()
	e := newPinEnv(t)
	d := e.pushN(t, 1)[0]
	calls := 0
	img, release, err := e.store.getAndHold(func() (*Image, error) {
		calls++
		img, err := e.store.getPinned(ctx, "tna", pin("tna/app", d))
		if err == nil && calls == 1 {
			os.Remove(img.Path) // a concurrent GC got there first
			os.Remove(strings.TrimSuffix(img.Path, ".erofs") + ".json")
		}
		return img, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if calls != 2 || !exists(t, img) {
		t.Fatalf("calls %d, exists %v", calls, exists(t, img))
	}
	if e.store.held[img.Path] != 1 {
		t.Fatalf("held %d, want 1 (the failed try's hold released)", e.store.held[img.Path])
	}
	_, _, err = e.store.getAndHold(func() (*Image, error) {
		img, err := e.store.getPinned(ctx, "tna", pin("tna/app", d))
		if err == nil {
			os.Remove(img.Path)
			os.Remove(strings.TrimSuffix(img.Path, ".erofs") + ".json")
		}
		return img, err
	})
	if err == nil || !strings.Contains(err.Error(), "evicted") {
		t.Fatalf("want an eviction error after %d tries, got %v", heldTries, err)
	}
}
