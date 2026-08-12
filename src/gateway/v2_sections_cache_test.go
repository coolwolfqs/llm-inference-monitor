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
	v2SectionsCache.Unlock()
}

func TestGetV2SectionsSharesConcurrentBuild(t *testing.T) {
	resetV2SectionsCache()
	var builds int32
	build := func() gin.H {
		atomic.AddInt32(&builds, 1)
		time.Sleep(10 * time.Millisecond)
		return gin.H{"build": builds}
	}
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := getV2Sections(now, build); got["build"] != int32(1) {
				t.Errorf("unexpected cached result: %v", got)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("build count = %d, want 1", got)
	}
}

func TestGetV2SectionsRefreshesAfterTTL(t *testing.T) {
	resetV2SectionsCache()
	var builds int32
	build := func() gin.H { return gin.H{"build": atomic.AddInt32(&builds, 1)} }
	now := time.Now()
	if got := getV2Sections(now, build)["build"]; got != int32(1) {
		t.Fatalf("first build = %v", got)
	}
	if got := getV2Sections(now.Add(v2SectionsCacheTTL-time.Millisecond), build)["build"]; got != int32(1) {
		t.Fatalf("cached build = %v", got)
	}
	if got := getV2Sections(now.Add(v2SectionsCacheTTL), build)["build"]; got != int32(2) {
		t.Fatalf("refreshed build = %v", got)
	}
}
