package quark

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	rclonecrypt "github.com/rclone/rclone/backend/crypt"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/video-site/backend/internal/drives"
)

// CryptConfig mirrors the user-facing rclone/OpenList Crypt settings. The
// password and salt are supplied as plaintext credentials and obscured only
// while constructing rclone's in-memory cipher configuration.
type CryptConfig struct {
	Password                string
	Salt                    string
	FilenameEncryption      string
	DirectoryNameEncryption bool
	FilenameEncoding        string
	Suffix                  string
}

type cryptFile struct {
	plaintextSize  int64
	encryptedSize  int64
	name           string
	contentType    string
	header         []byte
	rangeChecked   bool
	rangeSupported bool
}

// cryptFileHeaderSize is fixed by rclone's Crypt file format. Caching it
// avoids one remote read whenever rclone opens a non-zero plaintext range.
const (
	cryptFileHeaderSize    = 32
	cryptContentSniffBytes = 512
)

// CryptDriver retains its backing drive's public kind so scanner, catalog and
// crawler routing do not need a second provider type. It never returns a
// ciphertext StreamLink: plaintext is exposed only through
// PlaintextRangeProvider.
type CryptDriver struct {
	base   drives.Drive
	cipher *rclonecrypt.Cipher

	mu               sync.RWMutex
	files            map[string]cryptFile
	encryptedCache   *quarkCryptRangeCache
	prefetchMu       sync.Mutex
	prefetching      map[quarkCryptPrefetchKey]*quarkCryptPrefetch
	queuedPrefetches map[quarkCryptPrefetchKey]*quarkCryptPrefetch
	seekMu           sync.RWMutex
	seekTraces       map[string]*quarkCryptSeekTrace
	ciphertextMu     sync.Mutex
	activeCiphertext map[string]int
}

type quarkCryptPrefetchKey struct {
	fileID string
	offset int64
	length int64
}

type quarkCryptPrefetch struct {
	cancel context.CancelFunc
	ctx    context.Context
}

// quarkCryptLinkOpener acquires a download URL only when a ciphertext read is
// actually needed. A browser can make hundreds of tiny plaintext requests
// that are entirely served by the encrypted cache; obtaining a fresh Quark
// link before checking that cache adds avoidable latency to every one.
type quarkCryptLinkOpener func(context.Context) (*drives.StreamLink, error)

func staticQuarkCryptLinkOpener(link *drives.StreamLink) quarkCryptLinkOpener {
	return func(context.Context) (*drives.StreamLink, error) {
		if link == nil {
			return nil, errors.New("quark crypt: nil download link")
		}
		return link, nil
	}
}

const (
	// A small foreground range is already in flight, so retain a bounded
	// three-request ceiling for detached cache windows.
	quarkCryptLookaheadParts = 3
	// Reserve two detached cache slots for a small foreground playback range.
	quarkCryptSmallRangeLookaheadParts = quarkCryptPartConcurrency - 1
	// MP4 audio and video data can reside in distant byte regions. A smaller
	// detached window prevents the low-bitrate audio region from monopolizing
	// a full 10 MiB cache slot before the video region can recover.
	quarkCryptPlaybackPrefetchSize int64 = 4 * 1024 * 1024
	quarkCryptLookaheadTimeout           = 45 * time.Second
)

func NewCrypt(base drives.Drive, cfg CryptConfig) (*CryptDriver, error) {
	if base == nil {
		return nil, errors.New("nil crypt base drive")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return nil, errors.New("crypt password is required")
	}
	password, err := obscure.Obscure(cfg.Password)
	if err != nil {
		return nil, errors.New("unable to prepare crypt password")
	}
	options := configmap.Simple{
		"password":                  password,
		"filename_encryption":       defaultCryptValue(cfg.FilenameEncryption, "standard"),
		"directory_name_encryption": strconv.FormatBool(cfg.DirectoryNameEncryption),
		"filename_encoding":         defaultCryptValue(cfg.FilenameEncoding, "base64"),
		"suffix":                    defaultCryptValue(cfg.Suffix, ".bin"),
	}
	if cfg.Salt != "" {
		salt, err := obscure.Obscure(cfg.Salt)
		if err != nil {
			return nil, errors.New("unable to prepare crypt salt")
		}
		options["password2"] = salt
	}
	cipher, err := rclonecrypt.NewCipher(options)
	if err != nil {
		// Do not include options here: they contain obscured secrets and make
		// an admin-visible attach error needlessly hard to reason about.
		return nil, fmt.Errorf("invalid Crypt settings: %w", err)
	}
	return &CryptDriver{
		base:             base,
		cipher:           cipher,
		files:            make(map[string]cryptFile),
		encryptedCache:   newQuarkCryptRangeCache(),
		prefetching:      make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
		queuedPrefetches: make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch),
		seekTraces:       make(map[string]*quarkCryptSeekTrace),
		activeCiphertext: make(map[string]int),
	}, nil
}

func defaultCryptValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func (d *CryptDriver) Kind() string   { return d.base.Kind() }
func (d *CryptDriver) ID() string     { return d.base.ID() }
func (d *CryptDriver) RootID() string { return d.base.RootID() }
func (d *CryptDriver) Init(ctx context.Context) error {
	return d.base.Init(ctx)
}

func (d *CryptDriver) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	entries, err := d.base.List(ctx, dirID)
	if err != nil {
		return nil, err
	}
	out := make([]drives.Entry, 0, len(entries))
	for _, entry := range entries {
		plain, size, err := d.decryptEntry(entry)
		if err != nil {
			// A Crypt root can contain files created outside Crypt, stale partial
			// uploads, or objects encrypted with another password. Keep scanning
			// the remaining encrypted entries without exposing ciphertext names.
			continue
		}
		if !plain.IsDir {
			d.rememberFile(plain.ID, cryptFile{
				plaintextSize: size,
				encryptedSize: entry.Size,
				name:          plain.Name,
			})
		}
		out = append(out, plain)
	}
	return out, nil
}

func (d *CryptDriver) decryptEntry(entry drives.Entry) (drives.Entry, int64, error) {
	var (
		name string
		err  error
		size = entry.Size
	)
	if entry.IsDir {
		name, err = d.cipher.DecryptDirName(entry.Name)
	} else {
		name, err = d.cipher.DecryptFileName(entry.Name)
		if err == nil {
			size, err = d.cipher.DecryptedSize(entry.Size)
		}
	}
	if err != nil {
		return drives.Entry{}, 0, err
	}
	entry.Name = name
	entry.Size = size
	entry.MimeType = guessMime(name)
	return entry, size, nil
}

func (d *CryptDriver) Stat(ctx context.Context, fileID string) (*drives.Entry, error) {
	return nil, drives.ErrNotSupported
}

func (d *CryptDriver) StreamURL(_ context.Context, fileID string) (*drives.StreamLink, error) {
	if strings.TrimSpace(fileID) == "" {
		return nil, errors.New("quark crypt: empty file id")
	}
	return &drives.StreamLink{PlaintextSource: d, PlaintextFileID: fileID}, nil
}

func (d *CryptDriver) MakeDir(ctx context.Context, parentID, name string) (string, error) {
	base, ok := d.base.(interface {
		MakeDir(context.Context, string, string) (string, error)
	})
	if !ok {
		return "", drives.ErrNotSupported
	}
	return base.MakeDir(ctx, parentID, d.cipher.EncryptDirName(name))
}

func (d *CryptDriver) EnsureDir(ctx context.Context, pathFromRoot string) (string, error) {
	parts := splitPath(pathFromRoot)
	if len(parts) == 0 {
		return d.RootID(), nil
	}
	for i, name := range parts {
		parts[i] = d.cipher.EncryptDirName(name)
	}
	return d.base.EnsureDir(ctx, strings.Join(parts, "/"))
}

func (d *CryptDriver) Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("quark crypt: upload size is required")
	}
	encrypted, err := d.cipher.EncryptData(r)
	if err != nil {
		return "", fmt.Errorf("quark crypt: encrypt upload: %w", err)
	}
	fileID, err := d.base.Upload(ctx, parentID, d.cipher.EncryptFileName(name), encrypted, d.cipher.EncryptedSize(size))
	if err != nil {
		return "", err
	}
	d.rememberFile(fileID, cryptFile{
		plaintextSize: size,
		encryptedSize: d.cipher.EncryptedSize(size),
		name:          name,
		contentType:   guessMime(name),
	})
	return fileID, nil
}

func (d *CryptDriver) UploadAndReportSHA1(ctx context.Context, parentID, name string, r io.Reader, size int64) (UploadResult, error) {
	if size < 0 {
		return UploadResult{}, errors.New("quark crypt: upload size is required")
	}
	hash := sha1.New()
	encrypted, err := d.cipher.EncryptData(io.TeeReader(r, hash))
	if err != nil {
		return UploadResult{}, fmt.Errorf("quark crypt: encrypt upload: %w", err)
	}
	fileID, err := d.base.Upload(ctx, parentID, d.cipher.EncryptFileName(name), encrypted, d.cipher.EncryptedSize(size))
	if err != nil {
		return UploadResult{}, err
	}
	d.rememberFile(fileID, cryptFile{
		plaintextSize: size,
		encryptedSize: d.cipher.EncryptedSize(size),
		name:          name,
		contentType:   guessMime(name),
	})
	return UploadResult{FileID: fileID, SHA1: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func (d *CryptDriver) Rename(ctx context.Context, fileID, newName string) error {
	base, ok := d.base.(interface {
		Rename(context.Context, string, string) error
	})
	if !ok {
		return drives.ErrNotSupported
	}
	return base.Rename(ctx, fileID, d.cipher.EncryptFileName(newName))
}

func (d *CryptDriver) Remove(ctx context.Context, fileID string) error {
	base, ok := d.base.(drives.Remover)
	if !ok {
		return drives.ErrNotSupported
	}
	return base.Remove(ctx, fileID)
}

func (d *CryptDriver) PlaintextSize(ctx context.Context, fileID string) (int64, error) {
	d.mu.RLock()
	known, ok := d.files[fileID]
	d.mu.RUnlock()
	if ok && known.plaintextSize >= 0 && known.rangeChecked {
		return known.plaintextSize, nil
	}

	trace := d.activeSeekTrace(fileID)
	if trace != nil {
		trace.noteSizeProbe()
	}
	link, err := d.newDownloadLink(ctx, fileID, trace)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return 0, err
	}
	copyHeader(req.Header, link.Headers)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", cryptFileHeaderSize-1))
	active := d.beginCiphertextRange(fileID)
	if trace != nil {
		trace.noteActiveCiphertext(active)
	}
	upstreamStarted := time.Now()
	resp, err := streamClient(link).Do(req)
	if trace != nil {
		trace.noteUpstreamResponse(time.Since(upstreamStarted))
	}
	if err != nil {
		d.endCiphertextRange(fileID)
		return 0, fmt.Errorf("quark crypt: probe encrypted file: %w", err)
	}
	defer d.endCiphertextRange(fileID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("quark crypt: encrypted file probe returned HTTP %d", resp.StatusCode)
	}
	encryptedSize, err := encryptedLength(resp)
	if err != nil {
		return 0, err
	}
	plainSize, err := d.cipher.DecryptedSize(encryptedSize)
	if err != nil {
		return 0, fmt.Errorf("quark crypt: invalid encrypted file size: %w", err)
	}
	header := make([]byte, cryptFileHeaderSize)
	if n, readErr := io.ReadFull(resp.Body, header); n != cryptFileHeaderSize {
		return 0, fmt.Errorf("quark crypt: read encrypted header: got %d bytes: %w", n, readErr)
	}
	d.mu.Lock()
	known = d.files[fileID]
	known.plaintextSize = plainSize
	known.encryptedSize = encryptedSize
	known.header = header
	known.rangeChecked = true
	known.rangeSupported = quarkRangeResponseAt(resp, 0)
	d.files[fileID] = known
	d.mu.Unlock()
	return plainSize, nil
}

func (d *CryptDriver) OpenPlaintextRange(ctx context.Context, fileID string, offset, limit int64) (io.ReadCloser, error) {
	if offset < 0 || limit == 0 {
		return nil, errors.New("quark crypt: invalid plaintext range")
	}
	playback := drives.IsPlaintextPlaybackRequest(ctx)
	trace := d.activeSeekTrace(fileID)
	if trace != nil {
		trace.notePlainRange(offset, limit)
	}
	// Keep Quark links fresh between browser requests, but do not ask Quark for
	// one until the encrypted cache proves that this reader needs a CDN range.
	// Playback requests often finish entirely from a detached cache window.
	linkOpener := func(requestCtx context.Context) (*drives.StreamLink, error) {
		return d.newDownloadLink(requestCtx, fileID, trace)
	}
	decrypted, err := d.cipher.DecryptDataSeek(ctx, func(ctx context.Context, encryptedOffset, encryptedLimit int64) (io.ReadCloser, error) {
		return d.openEncryptedRangeWithLinkOpener(ctx, fileID, linkOpener, encryptedOffset, encryptedLimit, trace, playback)
	}, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("quark crypt: decrypt range: %w", err)
	}
	reader := io.ReadCloser(decrypted)
	if d.shouldSniffContentType(fileID, offset, limit) {
		reader, err = d.sniffContentType(fileID, reader)
		if err != nil {
			return nil, err
		}
	}
	if trace != nil {
		trace.noteOpenReady()
		reader = &quarkCryptFirstByteReader{ReadCloser: reader, driver: d, trace: trace}
	}
	return reader, nil
}

func (d *CryptDriver) PlaintextContentType(fileID string) string {
	d.mu.RLock()
	file, ok := d.files[fileID]
	d.mu.RUnlock()
	if !ok {
		return "application/octet-stream"
	}
	if file.contentType != "" {
		return file.contentType
	}
	return guessMime(file.name)
}

// PrioritizePlaintextSeek stops unfinished lookaheads for the requested file.
// The browser owns its foreground media ranges and will cancel the old one as
// it changes playback position. Cancelling it here can leave Chromium waiting
// without ever retrying the target Range.
func (d *CryptDriver) PrioritizePlaintextSeek(fileID string) {
	if fileID == "" {
		return
	}
	d.beginSeekTrace(fileID)
	d.prefetchMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(d.prefetching)+len(d.queuedPrefetches))
	for key, prefetch := range d.prefetching {
		if key.fileID == fileID && prefetch != nil && prefetch.cancel != nil {
			cancels = append(cancels, prefetch.cancel)
		}
	}
	for key, prefetch := range d.queuedPrefetches {
		if key.fileID != fileID {
			continue
		}
		delete(d.queuedPrefetches, key)
		if prefetch != nil && prefetch.cancel != nil {
			cancels = append(cancels, prefetch.cancel)
		}
	}
	d.prefetchMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

func (d *CryptDriver) shouldSniffContentType(fileID string, offset, limit int64) bool {
	if offset != 0 || (limit >= 0 && limit < cryptContentSniffBytes) {
		return false
	}
	d.mu.RLock()
	file, ok := d.files[fileID]
	d.mu.RUnlock()
	return !ok || (file.contentType == "" && guessMime(file.name) == "application/octet-stream")
}

func (d *CryptDriver) sniffContentType(fileID string, reader io.ReadCloser) (io.ReadCloser, error) {
	prefix, err := io.ReadAll(io.LimitReader(reader, cryptContentSniffBytes))
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("quark crypt: read plaintext type probe: %w", err)
	}
	if contentType := http.DetectContentType(prefix); contentType != "application/octet-stream" {
		d.rememberContentType(fileID, contentType)
	}
	return &limitedReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), reader), Closer: reader}, nil
}

func (d *CryptDriver) openEncryptedRange(ctx context.Context, fileID string, link *drives.StreamLink, offset, limit int64, trace *quarkCryptSeekTrace, playback bool) (io.ReadCloser, error) {
	return d.openEncryptedRangeWithLinkOpener(ctx, fileID, staticQuarkCryptLinkOpener(link), offset, limit, trace, playback)
}

func (d *CryptDriver) openEncryptedRangeWithLinkOpener(ctx context.Context, fileID string, linkOpener quarkCryptLinkOpener, offset, limit int64, trace *quarkCryptSeekTrace, playback bool) (io.ReadCloser, error) {
	if linkOpener == nil {
		return nil, errors.New("quark crypt: missing download link opener")
	}
	d.mu.RLock()
	known, hasKnownSize := d.files[fileID]
	d.mu.RUnlock()
	if hasKnownSize && known.encryptedSize > 0 && offset >= known.encryptedSize {
		return nil, errors.New("quark crypt: requested encrypted range is outside the upstream object")
	}
	if offset == 0 && limit > 0 && limit <= cryptFileHeaderSize && len(known.header) >= int(limit) {
		return io.NopCloser(bytes.NewReader(known.header[:limit])), nil
	}

	requestLimit := limit
	if offset == 0 && limit > 0 && limit <= cryptFileHeaderSize {
		requestLimit = cryptFileHeaderSize
	}
	if requestLimit < 0 && hasKnownSize && known.encryptedSize > 0 {
		requestLimit = known.encryptedSize - offset
	}
	if requestLimit == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if requestLimit < 0 {
		return nil, errors.New("quark crypt: encrypted range size is unknown")
	}
	if hasKnownSize && known.encryptedSize > 0 && offset+requestLimit > known.encryptedSize {
		requestLimit = known.encryptedSize - offset
	}

	openHTTPRange := func(requestCtx context.Context, requestOffset, requestLimit int64) (io.ReadCloser, error) {
		link, err := linkOpener(requestCtx)
		if err != nil {
			return nil, err
		}
		return d.openEncryptedHTTPRange(requestCtx, fileID, link, requestOffset, requestLimit, trace)
	}
	open := func(requestCtx context.Context, requestOffset, requestLimit int64) (io.ReadCloser, error) {
		if cached, hit, err := d.encryptedCache.Open(requestCtx, fileID, requestOffset, requestLimit, func(httpCtx context.Context, httpOffset, httpLimit int64) (io.ReadCloser, error) {
			return openHTTPRange(httpCtx, httpOffset, httpLimit)
		}); err != nil {
			return nil, err
		} else if hit {
			if trace != nil {
				trace.noteCacheHit()
			}
			return cached, nil
		}
		if trace != nil {
			trace.noteCacheMiss()
		}
		return openHTTPRange(requestCtx, requestOffset, requestLimit)
	}
	cachePart := func(partOffset int64, data []byte) {
		d.encryptedCache.Put(fileID, partOffset, data)
	}
	var (
		reader io.ReadCloser
		err    error
	)
	if known.rangeChecked && known.rangeSupported && requestLimit > quarkCryptPartSize {
		prefetchMax := quarkCryptPartConcurrency - 1
		if playback {
			// Browser media requests commonly use "bytes=<offset>-" and abandon
			// that reader after a small probe. Keep its foreground stream to one
			// range; the detached playback window below completes the reusable
			// 10 MiB cache entry without inheriting the browser cancellation.
			prefetchMax = 0
		} else {
			d.prefetchBeyondCachedRangeWithLinkOpener(fileID, linkOpener, offset, requestLimit)
		}
		reader, err = newEncryptedRangePrefetchReader(ctx, offset, requestLimit, open, cachePart, prefetchMax)
	} else {
		reader, err = open(ctx, offset, requestLimit)
	}
	if err != nil {
		return nil, err
	}
	if trace != nil && playback {
		trace.notePrefetchGate(playback, known.rangeChecked, known.rangeSupported, known.encryptedSize > 0)
	}
	if playback && known.rangeChecked && known.rangeSupported {
		windowParts := quarkCryptSmallRangeLookaheadParts
		if requestLimit > quarkCryptPartSize {
			windowParts = 1
		}
		d.prefetchPlaybackWindowWithLinkOpener(fileID, linkOpener, offset, known.encryptedSize, windowParts, trace)
	}
	if offset != 0 {
		return reader, nil
	}

	header := make([]byte, cryptFileHeaderSize)
	if n, readErr := io.ReadFull(reader, header); n != cryptFileHeaderSize {
		_ = reader.Close()
		return nil, fmt.Errorf("quark crypt: read encrypted header: got %d bytes: %w", n, readErr)
	}
	d.rememberHeader(fileID, header)
	if limit > 0 && limit <= cryptFileHeaderSize {
		_ = reader.Close()
		return io.NopCloser(bytes.NewReader(header[:limit])), nil
	}
	return &limitedReadCloser{Reader: io.MultiReader(bytes.NewReader(header), reader), Closer: reader}, nil
}

// openEncryptedHTTPRange performs one authenticated ciphertext request. Large
// reads are assembled by encryptedRangePrefetchReader using this opener.
func (d *CryptDriver) openEncryptedHTTPRange(ctx context.Context, fileID string, link *drives.StreamLink, offset, limit int64, trace *quarkCryptSeekTrace) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, err
	}
	copyHeader(req.Header, link.Headers)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1))
	active := d.beginCiphertextRange(fileID)
	if trace != nil {
		trace.noteActiveCiphertext(active)
	}
	upstreamStarted := time.Now()
	resp, err := streamClient(link).Do(req)
	if trace != nil {
		trace.noteUpstreamResponse(time.Since(upstreamStarted))
	}
	if err != nil {
		d.endCiphertextRange(fileID)
		return nil, fmt.Errorf("quark crypt: read encrypted range: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		d.endCiphertextRange(fileID)
		return nil, fmt.Errorf("quark crypt: encrypted range returned HTTP %d", resp.StatusCode)
	}
	rangeSupported := quarkRangeResponseAt(resp, offset)
	d.rememberRangeSupport(fileID, rangeSupported)
	if offset > 0 && !rangeSupported {
		// Some Quark CDN endpoints ignore Range and return the complete object
		// with 200. Keep playback working by discarding the prefix locally. This
		// is less efficient than a 206 response, but avoids failing large Crypt
		// files once rclone seeks past its initial block.
		if resp.ContentLength >= 0 && offset >= resp.ContentLength {
			_ = resp.Body.Close()
			d.endCiphertextRange(fileID)
			return nil, errors.New("quark crypt: requested encrypted range is outside the upstream object")
		}
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			_ = resp.Body.Close()
			d.endCiphertextRange(fileID)
			return nil, fmt.Errorf("quark crypt: skip unsupported upstream range: %w", err)
		}
	}
	body := &quarkCryptCiphertextReadCloser{
		ReadCloser: resp.Body,
		done: func() {
			d.endCiphertextRange(fileID)
		},
	}
	return &limitedReadCloser{Reader: io.LimitReader(body, limit), Closer: body}, nil
}

// prefetchBeyondCachedRange keeps the same three-part read-ahead window that
// OpenList's downloader keeps for a live range stream. A cache hit must not
// make that window disappear: otherwise the browser can drain cached bytes at
// LAN speed and hit an empty edge before Quark has started the next download.
func (d *CryptDriver) prefetchBeyondCachedRange(fileID string, link *drives.StreamLink, offset, limit int64) {
	d.prefetchBeyondCachedRangeWithLinkOpener(fileID, staticQuarkCryptLinkOpener(link), offset, limit)
}

func (d *CryptDriver) prefetchBeyondCachedRangeWithLinkOpener(fileID string, linkOpener quarkCryptLinkOpener, offset, limit int64) {
	if d.encryptedCache == nil || limit <= quarkCryptPartSize {
		return
	}
	end := offset + limit
	if end <= offset {
		return
	}
	start := d.encryptedCache.ContiguousEnd(fileID, offset, end)
	if start <= offset || start >= end {
		return
	}

	for i := 0; i < quarkCryptPartConcurrency && start < end; i++ {
		length := quarkCryptPartSize
		if remaining := end - start; remaining < length {
			length = remaining
		}
		if d.encryptedCache.Get(fileID, start, length) == nil {
			d.startEncryptedLookaheadWithLinkOpener(fileID, linkOpener, start, length, nil)
		}
		start += length
	}
}

// prefetchPlaybackWindow turns a browser's often open-ended media Range into
// detached 4 MiB cache windows. Those windows outlive a browser abandoning
// its first 32-512 KiB probe, so subsequent Range requests do not have to wait
// for a new Quark CDN connection. Once the current window is cached, continue
// at its contiguous end instead of repeatedly selecting that same window.
func (d *CryptDriver) prefetchPlaybackWindow(fileID string, link *drives.StreamLink, offset, encryptedSize int64, partCount int, trace *quarkCryptSeekTrace) {
	d.prefetchPlaybackWindowWithLinkOpener(fileID, staticQuarkCryptLinkOpener(link), offset, encryptedSize, partCount, trace)
}

func (d *CryptDriver) prefetchPlaybackWindowWithLinkOpener(fileID string, linkOpener quarkCryptLinkOpener, offset, encryptedSize int64, partCount int, trace *quarkCryptSeekTrace) {
	if d.encryptedCache == nil || encryptedSize <= 0 || offset < 0 || offset >= encryptedSize || partCount <= 0 {
		return
	}
	start := offset - offset%quarkCryptPlaybackPrefetchSize
	if cachedEnd := d.encryptedCache.ContiguousEnd(fileID, start, encryptedSize); cachedEnd > start {
		start = cachedEnd
	}
	for i := 0; i < partCount && start < encryptedSize; i++ {
		length := quarkCryptPlaybackPrefetchSize
		if remaining := encryptedSize - start; remaining < length {
			length = remaining
		}
		if d.encryptedCache.Get(fileID, start, length) == nil {
			d.startEncryptedLookaheadWithLinkOpener(fileID, linkOpener, start, length, trace)
		}
		start += length
	}
}

func (d *CryptDriver) startEncryptedLookahead(fileID string, link *drives.StreamLink, offset, length int64, trace *quarkCryptSeekTrace) {
	d.startEncryptedLookaheadWithLinkOpener(fileID, staticQuarkCryptLinkOpener(link), offset, length, trace)
}

func (d *CryptDriver) startEncryptedLookaheadWithLinkOpener(fileID string, linkOpener quarkCryptLinkOpener, offset, length int64, trace *quarkCryptSeekTrace) {
	if length <= 0 {
		return
	}
	key := quarkCryptPrefetchKey{fileID: fileID, offset: offset, length: length}
	ctx, cancel := context.WithTimeout(context.Background(), quarkCryptLookaheadTimeout)
	prefetch := &quarkCryptPrefetch{cancel: cancel, ctx: ctx}
	d.prefetchMu.Lock()
	if d.prefetching == nil {
		d.prefetching = make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch)
	}
	if d.queuedPrefetches == nil {
		d.queuedPrefetches = make(map[quarkCryptPrefetchKey]*quarkCryptPrefetch)
	}
	if existing, exists := d.prefetching[key]; exists && (existing == nil || existing.ctx == nil || existing.ctx.Err() == nil) {
		d.prefetchMu.Unlock()
		cancel()
		return
	}
	if _, exists := d.queuedPrefetches[key]; exists {
		d.prefetchMu.Unlock()
		cancel()
		return
	}
	if len(d.prefetching) >= quarkCryptLookaheadParts {
		if len(d.queuedPrefetches) < quarkCryptLookaheadParts {
			d.queuedPrefetches[key] = prefetch
			d.prefetchMu.Unlock()
			go d.waitForEncryptedLookaheadSlotWithLinkOpener(fileID, linkOpener, key, prefetch, trace)
			return
		}
		d.prefetchMu.Unlock()
		cancel()
		return
	}
	d.prefetching[key] = prefetch
	d.prefetchMu.Unlock()
	d.runEncryptedLookaheadWithLinkOpener(fileID, linkOpener, key, prefetch, trace)
}

// waitForEncryptedLookaheadSlot retains an intended cache window while a seek
// is cancelling stale downloads. Without this handoff the target's first
// small Range can find all slots occupied, skip its lookahead, and leave the
// browser to serially fetch 32-512 KiB ranges.
func (d *CryptDriver) waitForEncryptedLookaheadSlot(fileID string, link *drives.StreamLink, key quarkCryptPrefetchKey, prefetch *quarkCryptPrefetch, trace *quarkCryptSeekTrace) {
	d.waitForEncryptedLookaheadSlotWithLinkOpener(fileID, staticQuarkCryptLinkOpener(link), key, prefetch, trace)
}

func (d *CryptDriver) waitForEncryptedLookaheadSlotWithLinkOpener(fileID string, linkOpener quarkCryptLinkOpener, key quarkCryptPrefetchKey, prefetch *quarkCryptPrefetch, trace *quarkCryptSeekTrace) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-prefetch.ctx.Done():
			d.removeQueuedEncryptedLookahead(key, prefetch)
			if trace != nil {
				trace.notePrefetchCanceled()
			}
			return
		case <-ticker.C:
		}

		d.prefetchMu.Lock()
		if d.queuedPrefetches[key] != prefetch {
			d.prefetchMu.Unlock()
			return
		}
		if _, active := d.prefetching[key]; active || len(d.prefetching) >= quarkCryptLookaheadParts {
			d.prefetchMu.Unlock()
			continue
		}
		delete(d.queuedPrefetches, key)
		d.prefetching[key] = prefetch
		d.prefetchMu.Unlock()
		d.runEncryptedLookaheadWithLinkOpener(fileID, linkOpener, key, prefetch, trace)
		return
	}
}

func (d *CryptDriver) removeQueuedEncryptedLookahead(key quarkCryptPrefetchKey, prefetch *quarkCryptPrefetch) {
	d.prefetchMu.Lock()
	if d.queuedPrefetches[key] == prefetch {
		delete(d.queuedPrefetches, key)
	}
	d.prefetchMu.Unlock()
}

func (d *CryptDriver) runEncryptedLookahead(fileID string, link *drives.StreamLink, key quarkCryptPrefetchKey, prefetch *quarkCryptPrefetch, trace *quarkCryptSeekTrace) {
	d.runEncryptedLookaheadWithLinkOpener(fileID, staticQuarkCryptLinkOpener(link), key, prefetch, trace)
}

func (d *CryptDriver) runEncryptedLookaheadWithLinkOpener(fileID string, linkOpener quarkCryptLinkOpener, key quarkCryptPrefetchKey, prefetch *quarkCryptPrefetch, trace *quarkCryptSeekTrace) {
	if trace != nil {
		trace.notePrefetchStarted()
	}

	go func() {
		defer func() {
			d.prefetchMu.Lock()
			if d.prefetching[key] == prefetch {
				delete(d.prefetching, key)
			}
			d.prefetchMu.Unlock()
			prefetch.cancel()
		}()

		link, err := linkOpener(prefetch.ctx)
		if err != nil {
			if trace != nil {
				if prefetch.ctx.Err() != nil {
					trace.notePrefetchCanceled()
				} else {
					trace.notePrefetchFailed()
				}
			}
			return
		}
		body, err := d.openEncryptedHTTPRange(prefetch.ctx, fileID, link, key.offset, key.length, nil)
		if err != nil {
			if trace != nil {
				if prefetch.ctx.Err() != nil {
					trace.notePrefetchCanceled()
				} else {
					trace.notePrefetchFailed()
				}
			}
			return
		}
		part := newStreamingEncryptedPart(prefetch.ctx, body, key.length, nil)
		d.encryptedCache.AddLive(fileID, key.offset, key.length, part)
		if waitErr := part.wait(); waitErr != nil {
			d.encryptedCache.RemoveLive(fileID, key.offset, key.length, part)
			if trace != nil {
				if prefetch.ctx.Err() != nil {
					trace.notePrefetchCanceled()
				} else {
					trace.notePrefetchFailed()
				}
			}
			return
		}
		data := part.completedData()
		if int64(len(data)) != key.length {
			d.encryptedCache.RemoveLive(fileID, key.offset, key.length, part)
			if trace != nil {
				trace.notePrefetchFailed()
			}
			return
		}
		d.encryptedCache.Put(fileID, key.offset, data)
		d.encryptedCache.RemoveLive(fileID, key.offset, key.length, part)
		if trace != nil {
			trace.notePrefetchCompleted(int64(len(data)))
		}
	}()
}

// newDownloadLink returns a fresh backing-drive URL for a browser range request.
func (d *CryptDriver) newDownloadLink(ctx context.Context, fileID string, trace *quarkCryptSeekTrace) (*drives.StreamLink, error) {
	if fileID == "" {
		return nil, errors.New("quark crypt: empty file id")
	}
	started := time.Now()
	link, err := d.base.StreamURL(ctx, fileID)
	if trace != nil {
		trace.noteDownloadLink(time.Since(started))
	}
	if err != nil {
		return nil, err
	}
	if link == nil || strings.TrimSpace(link.URL) == "" {
		return nil, errors.New("quark crypt: empty download url")
	}
	return link, nil
}

type limitedReadCloser struct {
	io.Reader
	io.Closer
}

func (d *CryptDriver) rememberFile(fileID string, file cryptFile) {
	d.mu.Lock()
	if existing, ok := d.files[fileID]; ok {
		if len(file.header) == 0 {
			file.header = existing.header
		}
		if file.contentType == "" {
			file.contentType = existing.contentType
		}
		if !file.rangeChecked {
			file.rangeChecked = existing.rangeChecked
			file.rangeSupported = existing.rangeSupported
		}
	}
	d.files[fileID] = file
	d.mu.Unlock()
}

func (d *CryptDriver) rememberHeader(fileID string, header []byte) {
	if len(header) != cryptFileHeaderSize {
		return
	}
	d.mu.Lock()
	known := d.files[fileID]
	known.header = append(known.header[:0], header...)
	d.files[fileID] = known
	d.mu.Unlock()
}

func (d *CryptDriver) rememberContentType(fileID, contentType string) {
	d.mu.Lock()
	known := d.files[fileID]
	known.contentType = contentType
	d.files[fileID] = known
	d.mu.Unlock()
}

func (d *CryptDriver) rememberRangeSupport(fileID string, supported bool) {
	d.mu.Lock()
	known := d.files[fileID]
	known.rangeChecked = true
	known.rangeSupported = supported
	d.files[fileID] = known
	d.mu.Unlock()
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func streamClient(link *drives.StreamLink) *http.Client {
	if link != nil && link.HTTPClient != nil {
		return link.HTTPClient
	}
	return http.DefaultClient
}

func encryptedLength(resp *http.Response) (int64, error) {
	contentRange := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if slash := strings.LastIndex(contentRange, "/"); slash >= 0 {
		if size, err := strconv.ParseInt(contentRange[slash+1:], 10, 64); err == nil && size >= 0 {
			return size, nil
		}
	}
	if resp.StatusCode == http.StatusOK && resp.ContentLength >= 0 {
		return resp.ContentLength, nil
	}
	return 0, errors.New("quark crypt: upstream did not report encrypted file size")
}

var _ drives.Drive = (*CryptDriver)(nil)
var _ drives.PlaintextRangeProvider = (*CryptDriver)(nil)
var _ drives.PlaintextSeekPrioritizer = (*CryptDriver)(nil)
var _ drives.Remover = (*CryptDriver)(nil)
