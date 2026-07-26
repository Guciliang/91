package drives

import (
	"context"
	"testing"
)

func TestPlaintextPlaybackRequestContext(t *testing.T) {
	if IsPlaintextPlaybackRequest(context.Background()) {
		t.Fatal("background context was marked as playback")
	}
	if !IsPlaintextPlaybackRequest(WithPlaintextPlaybackRequest(context.Background())) {
		t.Fatal("playback context was not marked")
	}
}
