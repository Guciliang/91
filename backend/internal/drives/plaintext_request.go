package drives

import "context"

type plaintextPlaybackRequestContextKey struct{}

// WithPlaintextPlaybackRequest marks a plaintext range as browser playback.
// Transforming drives can use this distinction to stop a stale media Range
// after a user seek without interrupting previews or background workers.
func WithPlaintextPlaybackRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, plaintextPlaybackRequestContextKey{}, true)
}

// IsPlaintextPlaybackRequest reports whether ctx came from the browser stream
// proxy and may therefore be interrupted by a newer user seek.
func IsPlaintextPlaybackRequest(ctx context.Context) bool {
	playback, _ := ctx.Value(plaintextPlaybackRequestContextKey{}).(bool)
	return playback
}
