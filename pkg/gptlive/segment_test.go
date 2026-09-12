package gptlive

import (
	"testing"
	"time"
)

func TestSegmenterEmitsOpenAndCloseOnce(t *testing.T) {
	var opens, closes int
	seg := NewSegmenter(func() { opens++ }, func() { closes++ })
	quiet := tone(0.0005, 320)
	now := time.Unix(0, 0)
	seg.now = func() time.Time { return now }
	for i := 0; i < 50; i++ {
		seg.Feed(quiet, frame)
	}
	for i := 0; i < 10; i++ {
		seg.Feed(tone(0.3, 320), frame)
	}
	if opens != 1 || closes != 0 || !seg.Open() {
		t.Fatalf("after speech: opens=%d closes=%d open=%v", opens, closes, seg.Open())
	}
	// the model goes quiet without sending silence frames: the idle timer closes it
	seg.Tick(now.Add(2 * time.Second))
	if opens != 1 || closes != 1 || seg.Open() {
		t.Fatalf("after idle: opens=%d closes=%d open=%v", opens, closes, seg.Open())
	}
}
