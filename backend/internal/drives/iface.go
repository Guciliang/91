package drives

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Drive 是多家网盘统一抽象。上层不区分盘，只区分 Kind。
type Drive interface {
	// Kind 返回驱动代号："quark" / "p115" / "p123" / "pikpak" / "wopan" / "guangyapan" / "onedrive" / "googledrive" / "webdav" / "localstorage"
	Kind() string

	// ID 返回该盘在 catalog 中的唯一标识
	ID() string

	// Init 完成登录态校验；登录态由 Authenticator 另行获取后注入
	Init(ctx context.Context) error

	// List 列指定目录下的直接子项
	List(ctx context.Context, dirID string) ([]Entry, error)

	// Stat 拿到单个文件的元数据
	Stat(ctx context.Context, fileID string) (*Entry, error)

	// StreamURL 返回一次性直链 + 必须的请求头
	// 代理层据此回源，透传 Range
	StreamURL(ctx context.Context, fileID string) (*StreamLink, error)

	// RootID 返回根目录 fileID
	RootID() string
}

// GenerationStreamProvider is an optional drive capability for a provider-
// generated playback stream that is cheaper to seek than the original file.
// Background thumbnail/preview workers prefer this stream, while ordinary
// playback and fingerprinting continue to use StreamURL.
//
// forceRefresh invalidates any short-lived provider cache after a signed
// playlist rejection. Implementations must never expose account credentials in
// the returned StreamLink.
type GenerationStreamProvider interface {
	GenerationStreamURL(ctx context.Context, fileID string, forceRefresh bool) (*StreamLink, error)
}

// Uploader is the optional write capability of a drive. Callers that produce
// remote files must assert it before starting work instead of discovering an
// unsupported operation only after an expensive download/transcode has run.
type Uploader interface {
	Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error)
	EnsureDir(ctx context.Context, pathFromRoot string) (string, error)
}

// Remover is an optional drive capability. It mirrors OpenList's optional
// Remove interface: callers must type-assert before deleting a source file.
type Remover interface {
	Remove(ctx context.Context, fileID string) error
}

// SourceFile carries the catalog metadata available when an administrator
// requests deletion of the original source file.
type SourceFile struct {
	FileID   string
	ParentID string
	Name     string
	Size     int64
}

// SourceRemover is an optional, richer removal capability for providers whose
// playback ID is not the same ID required by their delete API.
type SourceRemover interface {
	RemoveSource(ctx context.Context, source SourceFile) error
}

type Entry struct {
	ID       string
	Name     string
	Size     int64
	Hash     string
	IsDir    bool
	ParentID string
	MimeType string
	ModTime  time.Time

	// 部分网盘额外信息
	Category     int    // 1=视频 (quark)
	ThumbnailURL string // 网盘侧已提供的快速缩略图
}

type StreamLink struct {
	URL     string
	Headers http.Header
	// Expires is the deadline through which callers may reuse this link. It can
	// be a conservative local deadline when a provider does not expose the
	// signed URL's exact expiry.
	Expires time.Time

	// HTTPClient is an optional per-drive client used for the final stream
	// request. It is intentionally not serialized: providers such as Quark can
	// require a private proxy for both their API and their media CDN.
	HTTPClient *http.Client `json:"-"`

	// PassThroughRedirects tells the online playback proxy to make the first
	// authenticated request itself, but relay an upstream 3xx Location to the
	// browser instead of following it on the server. Background consumers such
	// as fingerprinting and transcoding still follow redirects to read bytes.
	PassThroughRedirects bool

	// ClientRedirectSafe is an explicit per-link trust decision. It is true only
	// when URL alone authorizes the browser request and no secret Header values
	// are required after redirecting. This is intentionally not inferred from
	// drive Kind because redirect safety belongs to the returned link.
	ClientRedirectSafe bool

	// PlaintextSource is an internal-only stream source for transforms such as
	// Crypt. Consumers that need a URL (FFmpeg) create a short-lived loopback
	// proxy, while the playback proxy serves it directly. It is never exposed
	// in API JSON or redirect responses.
	PlaintextSource PlaintextRangeProvider `json:"-"`
	PlaintextFileID string                 `json:"-"`
}

// PlaintextRangeProvider is implemented by drives that transform remote bytes
// before clients can consume them. Ranges are expressed in plaintext bytes.
type PlaintextRangeProvider interface {
	PlaintextSize(ctx context.Context, fileID string) (int64, error)
	OpenPlaintextRange(ctx context.Context, fileID string, offset, limit int64) (io.ReadCloser, error)
}

// PlaintextSeekPrioritizer is an optional capability for transformed playback
// sources. The player invokes it after a user-initiated seek so a drive can
// stop stale browser playback and background work that would compete with the
// target range. Other plaintext consumers must remain uninterrupted.
type PlaintextSeekPrioritizer interface {
	PrioritizePlaintextSeek(fileID string)
}

// PlaintextSeekPlaybackReporter accepts one client-side result for a seek.
// The result is diagnostic only and must not affect playback behavior.
type PlaintextSeekPlaybackReporter interface {
	ReportPlaintextSeekPlayback(fileID string, wait, buffered time.Duration, readyState int)
}

// ErrNotSupported 代表某家盘不支持某操作
var ErrNotSupported = errors.New("operation not supported by this drive")

// ErrGenerationStreamUnavailable means the optional optimized generation
// stream does not exist for this file. Callers may safely fall back to the
// original StreamURL without treating the drive as unhealthy.
var ErrGenerationStreamUnavailable = errors.New("generation stream unavailable")

// RateLimitError 表示上游服务正在限流。RetryAfter 为 0 时由调用方选择默认冷却时间。
type RateLimitError struct {
	Provider   string
	RetryAfter time.Duration
	Err        error
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return "rate limited"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Provider != "" {
		return e.Provider + " rate limited"
	}
	return "rate limited"
}

func (e *RateLimitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func RateLimitRetryAfter(err error) (time.Duration, bool) {
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		return rateLimit.RetryAfter, true
	}
	return 0, false
}

// TextMentionsHTTPStatus only looks for explicit numeric HTTP status contexts
// in errors from tools that do not expose structured response metadata.
func TextMentionsHTTPStatus(text string, statuses ...int) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	for _, status := range statuses {
		if status <= 0 {
			continue
		}
		code := strconv.Itoa(status)
		if strings.HasPrefix(text, code+" ") ||
			strings.Contains(text, "status="+code) ||
			strings.Contains(text, "status: "+code) ||
			strings.Contains(text, "status "+code) ||
			strings.Contains(text, "status code "+code) ||
			strings.Contains(text, "http "+code) ||
			strings.Contains(text, "http status="+code) ||
			strings.Contains(text, "http status: "+code) ||
			strings.Contains(text, "http status "+code) ||
			strings.Contains(text, "server returned "+code) ||
			strings.Contains(text, "code="+code) ||
			strings.Contains(text, "code: "+code) ||
			strings.Contains(text, "error_code="+code) ||
			strings.Contains(text, "error_code: "+code) {
			return true
		}
	}
	return false
}

func ErrorMentionsHTTPStatus(err error, statuses ...int) bool {
	if err == nil {
		return false
	}
	return TextMentionsHTTPStatus(err.Error(), statuses...)
}

// ParseSingleByteRange parses the single byte range emitted by browsers and
// media tools. It clamps a valid end offset to the resource size and rejects
// multipart ranges because transformed streams can serve one range at a time.
func ParseSingleByteRange(raw string, size int64) (start, length int64, partial, valid bool) {
	if size < 0 {
		return 0, 0, false, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, size, false, true
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") || size == 0 {
		return 0, 0, false, false
	}
	values := strings.SplitN(strings.TrimPrefix(raw, "bytes="), "-", 2)
	if len(values) != 2 {
		return 0, 0, false, false
	}
	if values[0] == "" {
		suffix, err := strconv.ParseInt(values[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true, true
	}
	start, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, false
	}
	end := size - 1
	if values[1] != "" {
		end, err = strconv.ParseInt(values[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, true, true
}
