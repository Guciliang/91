package quark

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/video-site/backend/internal/drives"
)

const (
	defaultUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
	defaultReferer = "https://pan.quark.cn"
	defaultAPI     = "https://drive.quark.cn/1/clouddrive"
	defaultPR      = "ucpro"
)

type Driver struct {
	id                    string
	cookie                string
	rootID                string
	ua                    string
	referer               string
	apiBase               string
	pr                    string
	client                *resty.Client
	streamHTTPClient      *http.Client
	proxyErr              error
	uploadTempDir         string
	onCookieUpdate        func(string)
	useTranscodingAddress bool
}

type Config struct {
	ID                    string
	Cookie                string
	RootID                string
	UseTranscodingAddress bool   // 开启后对视频文件返回转码直链（支持 302），但可能画质不一致
	ProxyURL              string // HTTP(S) / SOCKS5 代理，仅用于此夸克盘
	UploadTempDir         string
	OnCookieUpdate        func(cookie string)
}

func New(c Config) *Driver {
	rootID := c.RootID
	if rootID == "" {
		rootID = "0"
	}
	d := &Driver{
		id:                    c.ID,
		cookie:                c.Cookie,
		rootID:                rootID,
		ua:                    defaultUA,
		referer:               defaultReferer,
		apiBase:               defaultAPI,
		pr:                    defaultPR,
		uploadTempDir:         c.UploadTempDir,
		useTranscodingAddress: c.UseTranscodingAddress,
		onCookieUpdate:        c.OnCookieUpdate,
	}
	d.client = resty.New().
		SetTimeout(30*time.Second).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Referer", d.referer).
		SetHeader("User-Agent", d.ua)
	if transport, err := transportForProxy(c.ProxyURL); err != nil {
		// Init returns this sanitized error before contacting Quark. Keeping New
		// non-failing preserves the driver construction contract used elsewhere.
		d.proxyErr = err
	} else {
		if transport == nil {
			transport = http.DefaultTransport.(*http.Transport).Clone()
		} else {
			d.client.SetTransport(transport)
		}
		configureStreamTransport(transport)
		d.streamHTTPClient = &http.Client{Transport: transport}
	}
	return d
}

func (d *Driver) Kind() string   { return "quark" }
func (d *Driver) ID() string     { return d.id }
func (d *Driver) RootID() string { return d.rootID }

// ---------- 公共请求 ----------

type resp struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (d *Driver) request(ctx context.Context, path, method string, query map[string]string, body any, out any) error {
	req := d.client.R().
		SetContext(ctx).
		SetHeader("Cookie", d.cookie).
		SetQueryParam("pr", d.pr).
		SetQueryParam("fr", "pc")
	if query != nil {
		req.SetQueryParams(query)
	}
	if body != nil {
		req.SetBody(body)
	}
	if out != nil {
		req.SetResult(out)
	}
	var e resp
	req.SetError(&e)

	res, err := req.Execute(method, d.apiBase+path)
	if err != nil {
		return err
	}

	// 处理 cookie 刷新（__puus）
	for _, ck := range res.Cookies() {
		if ck.Name == "__puus" {
			d.cookie = setCookieValue(d.cookie, "__puus", ck.Value)
			if d.onCookieUpdate != nil {
				d.onCookieUpdate(d.cookie)
			}
		}
	}

	if e.Status >= 400 || e.Code != 0 {
		if e.Message == "" {
			return fmt.Errorf("quark api error: status=%d code=%d", e.Status, e.Code)
		}
		return errors.New(e.Message)
	}
	return nil
}

func (d *Driver) Init(ctx context.Context) error {
	if d.proxyErr != nil {
		return d.proxyErr
	}
	return d.request(ctx, "/config", http.MethodGet, nil, nil, nil)
}

// ---------- 列目录 ----------

type file struct {
	Fid       string `json:"fid"`
	FileName  string `json:"file_name"`
	Size      int64  `json:"size"`
	Category  int    `json:"category"`
	File      bool   `json:"file"`
	UpdatedAt int64  `json:"updated_at"`
}

type sortResp struct {
	Data struct {
		List []file `json:"list"`
	} `json:"data"`
	Metadata struct {
		Total int `json:"_total"`
	} `json:"metadata"`
}

func (d *Driver) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	var out []drives.Entry
	page := 1
	size := 100
	for {
		q := map[string]string{
			"pdir_fid":             dirID,
			"_size":                strconv.Itoa(size),
			"_page":                strconv.Itoa(page),
			"_fetch_total":         "1",
			"fetch_all_file":       "1",
			"fetch_risk_file_name": "1",
		}
		var r sortResp
		if err := d.request(ctx, "/file/sort", http.MethodGet, q, nil, &r); err != nil {
			return nil, fmt.Errorf("quark list: %w", err)
		}
		for _, f := range r.Data.List {
			out = append(out, fileToEntry(&f, dirID))
		}
		if page*size >= r.Metadata.Total {
			break
		}
		page++
	}
	return out, nil
}

func (d *Driver) Stat(ctx context.Context, fileID string) (*drives.Entry, error) {
	// 夸克没提供单文件查询接口，回退到父目录遍历需要额外信息
	return nil, drives.ErrNotSupported
}

// ---------- 下载直链 ----------

type downResp struct {
	Data []struct {
		DownloadUrl string `json:"download_url"`
	} `json:"data"`
}

func (d *Driver) StreamURL(ctx context.Context, fileID string) (*drives.StreamLink, error) {
	body := map[string]any{"fids": []string{fileID}}
	var r downResp
	if err := d.request(ctx, "/file/download", http.MethodPost, nil, body, &r); err != nil {
		return nil, fmt.Errorf("quark download: %w", err)
	}
	if len(r.Data) == 0 || r.Data[0].DownloadUrl == "" {
		return nil, errors.New("quark download: empty url")
	}

	headers := http.Header{}
	headers.Set("User-Agent", d.ua)
	headers.Set("Referer", d.referer)
	headers.Set("Cookie", d.cookie)

	return &drives.StreamLink{
		URL:        r.Data[0].DownloadUrl,
		Headers:    headers,
		Expires:    time.Now().Add(10 * time.Minute),
		HTTPClient: d.streamHTTPClient,
	}, nil
}

// ---------- 创建目录 ----------

type mkdirResp struct {
	Data struct {
		Fid string `json:"fid"`
	} `json:"data"`
}

func (d *Driver) MakeDir(ctx context.Context, parentID, name string) (string, error) {
	body := map[string]any{
		"dir_init_lock": false,
		"dir_path":      "",
		"file_name":     name,
		"pdir_fid":      parentID,
	}
	var r mkdirResp
	if err := d.request(ctx, "/file", http.MethodPost, nil, body, &r); err != nil {
		return "", fmt.Errorf("quark mkdir: %w", err)
	}
	return r.Data.Fid, nil
}

func (d *Driver) EnsureDir(ctx context.Context, pathFromRoot string) (string, error) {
	parts := splitPath(pathFromRoot)
	currentID := d.rootID
	for _, name := range parts {
		childID, err := d.findChildDir(ctx, currentID, name)
		if err != nil {
			return "", err
		}
		if childID == "" {
			id, err := d.MakeDir(ctx, currentID, name)
			if err != nil {
				return "", err
			}
			childID = id
		}
		currentID = childID
	}
	return currentID, nil
}

func (d *Driver) findChildDir(ctx context.Context, parent, name string) (string, error) {
	entries, err := d.List(ctx, parent)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir && e.Name == name {
			return e.ID, nil
		}
	}
	return "", nil
}

// ---------- 上传 ----------

// UploadResult is returned to callers that persist a source-content
// fingerprint, such as the crawler migration worker.
type UploadResult struct {
	FileID string
	SHA1   string
	Size   int64
}

type uploadPreResp struct {
	Data struct {
		TaskID    string          `json:"task_id"`
		UploadID  string          `json:"upload_id"`
		ObjKey    string          `json:"obj_key"`
		UploadURL string          `json:"upload_url"`
		FID       string          `json:"fid"`
		Bucket    string          `json:"bucket"`
		Callback  json.RawMessage `json:"callback"`
		AuthInfo  string          `json:"auth_info"`
	} `json:"data"`
	Metadata struct {
		PartSize int64 `json:"part_size"`
	} `json:"metadata"`
}

type uploadHashResp struct {
	Data struct {
		Finish bool   `json:"finish"`
		FID    string `json:"fid"`
	} `json:"data"`
}

type uploadAuthResp struct {
	Data struct {
		AuthKey string `json:"auth_key"`
	} `json:"data"`
}

func (d *Driver) Upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (string, error) {
	result, err := d.upload(ctx, parentID, name, r, size)
	if err != nil {
		return "", err
	}
	return result.FileID, nil
}

// UploadAndReportSHA1 reports the hash of the supplied data, rather than the
// transport bytes, which keeps crawler deduplication stable with Crypt.
func (d *Driver) UploadAndReportSHA1(ctx context.Context, parentID, name string, r io.Reader, size int64) (UploadResult, error) {
	return d.upload(ctx, parentID, name, r, size)
}

func (d *Driver) upload(ctx context.Context, parentID, name string, r io.Reader, size int64) (UploadResult, error) {
	if strings.TrimSpace(parentID) == "" {
		return UploadResult{}, errors.New("quark upload: empty parent id")
	}
	if strings.TrimSpace(name) == "" {
		return UploadResult{}, errors.New("quark upload: empty file name")
	}
	if r == nil {
		return UploadResult{}, errors.New("quark upload: empty reader")
	}

	tmp, md5Hex, sha1Hex, written, err := d.cacheUpload(r, size)
	if err != nil {
		return UploadResult{}, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	pre, err := d.uploadPre(ctx, parentID, name, written)
	if err != nil {
		return UploadResult{}, err
	}
	finished, finishedID, err := d.uploadHash(ctx, md5Hex, sha1Hex, pre.Data.TaskID)
	if err != nil {
		return UploadResult{}, err
	}
	if finished {
		fileID := firstNonEmpty(finishedID, pre.Data.FID)
		if fileID == "" {
			fileID, err = d.findUploadedFile(ctx, parentID, name)
			if err != nil {
				return UploadResult{}, err
			}
		}
		return UploadResult{FileID: fileID, SHA1: sha1Hex, Size: written}, nil
	}
	if pre.Metadata.PartSize <= 0 || pre.Data.UploadID == "" || pre.Data.ObjKey == "" || pre.Data.Bucket == "" || pre.Data.UploadURL == "" {
		return UploadResult{}, errors.New("quark upload: invalid multipart task")
	}

	partCount := int((written + pre.Metadata.PartSize - 1) / pre.Metadata.PartSize)
	etags := make([]string, 0, partCount)
	for index := 0; index < partCount; index++ {
		if err := ctx.Err(); err != nil {
			return UploadResult{}, err
		}
		offset := int64(index) * pre.Metadata.PartSize
		length := minInt64(pre.Metadata.PartSize, written-offset)
		etag, err := d.uploadPart(ctx, pre, guessMime(name), index+1, io.NewSectionReader(tmp, offset, length))
		if err != nil {
			return UploadResult{}, err
		}
		etags = append(etags, etag)
	}
	if err := d.uploadCommit(ctx, pre, etags); err != nil {
		return UploadResult{}, err
	}
	if err := d.uploadFinish(ctx, pre); err != nil {
		return UploadResult{}, err
	}
	fileID := pre.Data.FID
	if fileID == "" {
		fileID, err = d.findUploadedFile(ctx, parentID, name)
		if err != nil {
			return UploadResult{}, err
		}
	}
	return UploadResult{FileID: fileID, SHA1: sha1Hex, Size: written}, nil
}

func (d *Driver) cacheUpload(r io.Reader, expectedSize int64) (*os.File, string, string, int64, error) {
	dir := d.uploadTempDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", "", 0, fmt.Errorf("quark upload: create temp dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "quark-upload-*")
	if err != nil {
		return nil, "", "", 0, fmt.Errorf("quark upload: create temp file: %w", err)
	}
	cleanup := func(err error) (*os.File, string, string, int64, error) {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", "", 0, err
	}
	md5Hash, sha1Hash := md5.New(), sha1.New()
	written, err := io.Copy(io.MultiWriter(tmp, md5Hash, sha1Hash), r)
	if err != nil {
		return cleanup(fmt.Errorf("quark upload: cache source: %w", err))
	}
	if expectedSize >= 0 && expectedSize != written {
		return cleanup(fmt.Errorf("quark upload: source size changed: got %d want %d", written, expectedSize))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return cleanup(fmt.Errorf("quark upload: rewind source: %w", err))
	}
	return tmp, hex.EncodeToString(md5Hash.Sum(nil)), hex.EncodeToString(sha1Hash.Sum(nil)), written, nil
}

func (d *Driver) uploadPre(ctx context.Context, parentID, name string, size int64) (uploadPreResp, error) {
	now := time.Now().UnixMilli()
	var out uploadPreResp
	err := d.request(ctx, "/file/upload/pre", http.MethodPost, nil, map[string]any{
		"ccp_hash_update": true, "dir_name": "", "file_name": name,
		"format_type": guessMime(name), "l_created_at": now, "l_updated_at": now,
		"pdir_fid": parentID, "size": size,
	}, &out)
	return out, err
}

func (d *Driver) uploadHash(ctx context.Context, md5Hex, sha1Hex, taskID string) (bool, string, error) {
	var out uploadHashResp
	err := d.request(ctx, "/file/update/hash", http.MethodPost, nil, map[string]string{
		"md5": md5Hex, "sha1": sha1Hex, "task_id": taskID,
	}, &out)
	return out.Data.Finish, out.Data.FID, err
}

func (d *Driver) uploadPart(ctx context.Context, pre uploadPreResp, mimeType string, number int, body io.Reader) (string, error) {
	now := time.Now().UTC().Format(http.TimeFormat)
	authMeta := fmt.Sprintf("PUT\n\n%s\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?partNumber=%d&uploadId=%s", mimeType, now, now, pre.Data.Bucket, pre.Data.ObjKey, number, pre.Data.UploadID)
	var auth uploadAuthResp
	if err := d.request(ctx, "/file/upload/auth", http.MethodPost, nil, map[string]string{
		"auth_info": pre.Data.AuthInfo, "auth_meta": authMeta, "task_id": pre.Data.TaskID,
	}, &auth); err != nil {
		return "", err
	}
	u, err := ossURL(pre)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("partNumber", strconv.Itoa(number))
	q.Set("uploadId", pre.Data.UploadID)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), body)
	if err != nil {
		return "", fmt.Errorf("quark upload: build part request: %w", err)
	}
	req.Header.Set("Authorization", auth.Data.AuthKey)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Referer", d.referer+"/")
	req.Header.Set("x-oss-date", now)
	req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
	resp, err := d.uploadHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("quark upload: send part: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("quark upload: part returned HTTP %d", resp.StatusCode)
	}
	etag := strings.TrimSpace(resp.Header.Get("Etag"))
	if etag == "" {
		return "", errors.New("quark upload: part response has no etag")
	}
	return etag, nil
}

type completeMultipartUpload struct {
	XMLName xml.Name                `xml:"CompleteMultipartUpload"`
	Parts   []completeMultipartPart `xml:"Part"`
}

type completeMultipartPart struct {
	Number int    `xml:"PartNumber"`
	ETag   string `xml:"ETag"`
}

func (d *Driver) uploadCommit(ctx context.Context, pre uploadPreResp, etags []string) error {
	parts := make([]completeMultipartPart, 0, len(etags))
	for index, etag := range etags {
		parts = append(parts, completeMultipartPart{Number: index + 1, ETag: etag})
	}
	body, err := xml.Marshal(completeMultipartUpload{Parts: parts})
	if err != nil {
		return fmt.Errorf("quark upload: encode complete request: %w", err)
	}
	body = append([]byte(xml.Header), body...)
	sum := md5.Sum(body)
	contentMD5 := base64.StdEncoding.EncodeToString(sum[:])
	callback := base64.StdEncoding.EncodeToString(pre.Data.Callback)
	now := time.Now().UTC().Format(http.TimeFormat)
	authMeta := fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?uploadId=%s", contentMD5, now, callback, now, pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadID)
	var auth uploadAuthResp
	if err := d.request(ctx, "/file/upload/auth", http.MethodPost, nil, map[string]string{
		"auth_info": pre.Data.AuthInfo, "auth_meta": authMeta, "task_id": pre.Data.TaskID,
	}, &auth); err != nil {
		return err
	}
	u, err := ossURL(pre)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("uploadId", pre.Data.UploadID)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("quark upload: build commit request: %w", err)
	}
	req.Header.Set("Authorization", auth.Data.AuthKey)
	req.Header.Set("Content-MD5", contentMD5)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Referer", d.referer+"/")
	req.Header.Set("x-oss-callback", callback)
	req.Header.Set("x-oss-date", now)
	req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
	resp, err := d.uploadHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("quark upload: commit parts: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("quark upload: commit returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (d *Driver) uploadFinish(ctx context.Context, pre uploadPreResp) error {
	return d.request(ctx, "/file/upload/finish", http.MethodPost, nil, map[string]string{
		"obj_key": pre.Data.ObjKey, "task_id": pre.Data.TaskID,
	}, nil)
}

func (d *Driver) findUploadedFile(ctx context.Context, parentID, name string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		entries, err := d.List(ctx, parentID)
		if err != nil {
			lastErr = err
		} else {
			for _, entry := range entries {
				if !entry.IsDir && entry.Name == name {
					return entry.ID, nil
				}
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("quark upload: look up completed file: %w", lastErr)
	}
	return "", errors.New("quark upload: completed file was not returned by provider")
}

func (d *Driver) uploadHTTPClient() *http.Client {
	if d.streamHTTPClient != nil {
		return d.streamHTTPClient
	}
	return http.DefaultClient
}

func ossURL(pre uploadPreResp) (*url.URL, error) {
	u, err := url.Parse(pre.Data.UploadURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("quark upload: invalid OSS endpoint")
	}
	if !strings.HasPrefix(u.Host, pre.Data.Bucket+".") {
		u.Host = pre.Data.Bucket + "." + u.Host
	}
	u.Path = "/" + strings.TrimLeft(pre.Data.ObjKey, "/")
	return u, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (d *Driver) Rename(ctx context.Context, fileID, newName string) error {
	fileID = strings.TrimSpace(fileID)
	newName = strings.TrimSpace(newName)
	if fileID == "" {
		return errors.New("quark rename: empty file id")
	}
	if newName == "" {
		return errors.New("quark rename: empty file name")
	}
	if err := d.request(ctx, "/file/rename", http.MethodPost, nil, map[string]string{
		"fid": fileID, "file_name": newName,
	}, nil); err != nil {
		return fmt.Errorf("quark rename: %w", err)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, fileID string) error {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return errors.New("quark remove: empty file id")
	}
	body := map[string]any{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{fileID},
	}
	if err := d.request(ctx, "/file/delete", http.MethodPost, nil, body, nil); err != nil {
		return fmt.Errorf("quark remove: %w", err)
	}
	return nil
}

// ---------- helpers ----------

func fileToEntry(f *file, parentID string) drives.Entry {
	return drives.Entry{
		ID:       f.Fid,
		Name:     f.FileName,
		Size:     f.Size,
		IsDir:    !f.File,
		ParentID: parentID,
		MimeType: guessMime(f.FileName),
		ModTime:  time.UnixMilli(f.UpdatedAt),
		Category: f.Category,
	}
}

func guessMime(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	}
	return "application/octet-stream"
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// setCookieValue 替换 cookie 字符串中某个 key 的值，不存在则追加
func setCookieValue(cookie, key, value string) string {
	if cookie == "" {
		return key + "=" + value
	}
	parts := strings.Split(cookie, ";")
	var out []string
	found := false
	for _, p := range parts {
		kv := strings.TrimSpace(p)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			out = append(out, kv)
			continue
		}
		if kv[:eq] == key {
			out = append(out, key+"="+value)
			found = true
		} else {
			out = append(out, kv)
		}
	}
	if !found {
		out = append(out, key+"="+value)
	}
	return strings.Join(out, "; ")
}

var _ drives.Drive = (*Driver)(nil)
var _ drives.Remover = (*Driver)(nil)
