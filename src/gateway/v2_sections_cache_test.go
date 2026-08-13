package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func resetV2SectionsCache() {
	v2SectionsCache.Lock()
	v2SectionsCache.data = nil
	v2SectionsCache.at = time.Time{}
	v2SectionsCache.refreshing = false
	v2SectionsCache.Unlock()
}

func waitForV2SectionsBuild(t *testing.T, want int32) gin.H {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v2SectionsCache.RLock()
		data := v2SectionsCache.data
		refreshing := v2SectionsCache.refreshing
		v2SectionsCache.RUnlock()
		if !refreshing && data != nil && data["build"] == want {
			return data
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for v2 sections build %d", want)
	return nil
}

func TestGetV2SectionsSharesConcurrentBuild(t *testing.T) {
	resetV2SectionsCache()
	var builds int32
	build := func() gin.H {
		atomic.AddInt32(&builds, 1)
		time.Sleep(10 * time.Millisecond)
		return gin.H{"build": builds}
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := getV2Sections(build); got == nil {
				t.Errorf("unexpected nil result")
			}
		}()
	}
	wg.Wait()
	waitForV2SectionsBuild(t, 1)
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("build count = %d, want 1", got)
	}
}

func TestGetV2SectionsRefreshesAfterTTL(t *testing.T) {
	resetV2SectionsCache()
	var builds int32
	build := func() gin.H { return gin.H{"build": atomic.AddInt32(&builds, 1)} }
	getV2Sections(build)
	waitForV2SectionsBuild(t, 1)
	if got := getV2Sections(build)["build"]; got != int32(1) {
		t.Fatalf("cached build = %v", got)
	}
	v2SectionsCache.Lock()
	v2SectionsCache.at = time.Now().Add(-2 * v2SectionsCacheTTL)
	v2SectionsCache.Unlock()
	if got := getV2Sections(build)["build"]; got != int32(1) {
		t.Fatalf("stale build = %v", got)
	}
	waitForV2SectionsBuild(t, 2)
	if got := atomic.LoadInt32(&builds); got != 2 {
		t.Fatalf("build count = %d, want 2", got)
	}
}

func TestGetV2SectionsReturnsStaleDataWhileRefreshing(t *testing.T) {
	resetV2SectionsCache()
	var builds int32
	build := func() gin.H { return gin.H{"build": atomic.AddInt32(&builds, 1)} }
	getV2Sections(build)
	waitForV2SectionsBuild(t, 1)

	v2SectionsCache.Lock()
	v2SectionsCache.at = time.Now().Add(-2 * v2SectionsCacheTTL)
	v2SectionsCache.Unlock()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	startedAt := time.Now()
	got := getV2Sections(func() gin.H {
		started <- struct{}{}
		<-release
		return gin.H{"build": atomic.AddInt32(&builds, 1)}
	})
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("stale request blocked for %v", elapsed)
	}
	if got["build"] != int32(1) {
		t.Fatalf("got build = %v, want stale build 1", got["build"])
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	close(release)
	waitForV2SectionsBuild(t, 2)
}
