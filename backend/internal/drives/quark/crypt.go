package quark

import (
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
	plaintextSize int64
	name          string
}

// CryptDriver keeps Quark as its public kind so scanner, catalog and crawler
// routing do not need a second provider type. It never returns a ciphertext
// StreamLink: plaintext is exposed only through PlaintextRangeProvider.
type CryptDriver struct {
	base   *Driver
	cipher *rclonecrypt.Cipher

	mu    sync.RWMutex
	files map[string]cryptFile
}

func NewCrypt(base *Driver, cfg CryptConfig) (*CryptDriver, error) {
	if base == nil {
		return nil, errors.New("nil quark driver")
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
	return &CryptDriver{base: base, cipher: cipher, files: make(map[string]cryptFile)}, nil
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
			d.rememberFile(plain.ID, cryptFile{plaintextSize: size, name: plain.Name})
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
	return d.base.MakeDir(ctx, parentID, d.cipher.EncryptDirName(name))
}

func (d *CryptDriver) EnsureDir(ctx context.Context, pathFromRoot string) (string, error) {
	currentID := d.RootID()
	for _, name := range splitPath(pathFromRoot) {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		childID := ""
		for _, entry := range entries {
			if entry.IsDir && entry.Name == name {
				childID = entry.ID
				break
			}
		}
		if childID == "" {
			childID, err = d.MakeDir(ctx, currentID, name)
			if err != nil {
				return "", err
			}
		}
		currentID = childID
	}
	return currentID, nil
}

func (d *CryptDriver) Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("quark crypt: upload size is required")
	}
	encrypted, err := d.cipher.EncryptData(r)
	if err != nil {
		return "", fmt.Errorf("quark crypt: encrypt upload: %w", err)
	}
	return d.base.Upload(ctx, parentID, d.cipher.EncryptFileName(name), encrypted, d.cipher.EncryptedSize(size))
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
	return UploadResult{FileID: fileID, SHA1: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func (d *CryptDriver) Rename(ctx context.Context, fileID, newName string) error {
	return d.base.Rename(ctx, fileID, d.cipher.EncryptFileName(newName))
}

func (d *CryptDriver) Remove(ctx context.Context, fileID string) error {
	return d.base.Remove(ctx, fileID)
}

func (d *CryptDriver) PlaintextSize(ctx context.Context, fileID string) (int64, error) {
	d.mu.RLock()
	known, ok := d.files[fileID]
	d.mu.RUnlock()
	if ok && known.plaintextSize >= 0 {
		return known.plaintextSize, nil
	}

	link, err := d.base.StreamURL(ctx, fileID)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return 0, err
	}
	copyHeader(req.Header, link.Headers)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := streamClient(link).Do(req)
	if err != nil {
		return 0, fmt.Errorf("quark crypt: probe encrypted file: %w", err)
	}
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
	d.mu.Lock()
	known = d.files[fileID]
	known.plaintextSize = plainSize
	d.files[fileID] = known
	d.mu.Unlock()
	return plainSize, nil
}

func (d *CryptDriver) OpenPlaintextRange(ctx context.Context, fileID string, offset, limit int64) (io.ReadCloser, error) {
	if offset < 0 || limit == 0 {
		return nil, errors.New("quark crypt: invalid plaintext range")
	}
	reader, err := d.cipher.DecryptDataSeek(ctx, func(ctx context.Context, encryptedOffset, encryptedLimit int64) (io.ReadCloser, error) {
		return d.openEncryptedRange(ctx, fileID, encryptedOffset, encryptedLimit)
	}, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("quark crypt: decrypt range: %w", err)
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
	return guessMime(file.name)
}

func (d *CryptDriver) openEncryptedRange(ctx context.Context, fileID string, offset, limit int64) (io.ReadCloser, error) {
	link, err := d.base.StreamURL(ctx, fileID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, err
	}
	copyHeader(req.Header, link.Headers)
	if limit >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := streamClient(link).Do(req)
	if err != nil {
		return nil, fmt.Errorf("quark crypt: read encrypted range: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("quark crypt: encrypted range returned HTTP %d", resp.StatusCode)
	}
	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, errors.New("quark crypt: upstream does not support ranges")
	}
	return resp.Body, nil
}

func (d *CryptDriver) rememberFile(fileID string, file cryptFile) {
	d.mu.Lock()
	d.files[fileID] = file
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
var _ drives.Remover = (*CryptDriver)(nil)
