package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3 is a client for an S3-compatible object store (AWS S3, MinIO, Ceph RGW
// and others) using path-style addressing and Signature Version 4, written on
// the standard library so the service takes on no SDK dependency. Objects
// larger than PartSize are sent as a multipart upload, one part in memory at a
// time. The bucket must be private: this client never sets a public ACL and
// never creates a bucket.
type S3 struct {
	Endpoint  string // e.g. https://s3.eu-central-1.amazonaws.com or http://minio:9000
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PartSize  int // bytes per multipart part; 0 = 8 MiB (S3's minimum is 5 MiB)
	Client    *http.Client
	now       func() time.Time
}

func (s *S3) Kind() string { return "s3" }

func (s *S3) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func hexSHA(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// s3Escape is RFC 3986 escaping as Signature V4 requires.
func s3Escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~', c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// do signs and sends one request. body may be nil.
func (s *S3) do(ctx context.Context, method, key string, query url.Values, body []byte, extra map[string]string) (*http.Response, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	return s.send(ctx, method, "/"+s3Escape(s.Bucket, false)+"/"+s3Escape(key, true), query, body, extra)
}

// send signs and sends a request for an already-escaped path.
func (s *S3) send(ctx context.Context, method, path string, query url.Values, body []byte, extra map[string]string) (*http.Response, error) {
	u, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("blob: invalid S3 endpoint %q", s.Endpoint)
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		cq = append(cq, s3Escape(k, false)+"="+s3Escape(query.Get(k), false))
	}
	canonQuery := strings.Join(cq, "&")
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	amzDate, day := now.Format("20060102T150405Z"), now.Format("20060102")
	payload := hexSHA(body)
	hdr := map[string]string{"host": u.Host, "x-amz-content-sha256": payload, "x-amz-date": amzDate}
	for k, v := range extra {
		hdr[strings.ToLower(k)] = v
	}
	names := make([]string, 0, len(hdr))
	for k := range hdr {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + strings.TrimSpace(hdr[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	canon := strings.Join([]string{method, path, canonQuery, ch.String(), signed, payload}, "\n")
	scope := day + "/" + s.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA([]byte(canon))
	k := hmacSHA(hmacSHA(hmacSHA(hmacSHA([]byte("AWS4"+s.SecretKey), day), s.Region), "s3"), "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))

	full := u.Scheme + "://" + u.Host + path
	if canonQuery != "" {
		full += "?" + canonQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		if k != "host" {
			req.Header.Set(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	return s.client().Do(req)
}

func s3Err(resp *http.Response, what string) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Code string `xml:"Code"`
	}
	_ = xml.Unmarshal(b, &e)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusPreconditionFailed || e.Code == "PreconditionFailed":
		return ErrExists
	}
	return fmt.Errorf("blob: S3 %s failed: HTTP %d %s", what, resp.StatusCode, e.Code)
}

func (s *S3) exists(ctx context.Context, key string) (bool, error) {
	resp, err := s.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("blob: S3 head failed: HTTP %d", resp.StatusCode)
}

// Put stores the object once. The write is conditional (If-None-Match: *)
// where the store supports it, and checked first for stores that ignore it.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	if taken, err := s.exists(ctx, key); err != nil {
		return 0, err
	} else if taken {
		return 0, ErrExists
	}
	part := s.PartSize
	if part == 0 {
		part = 8 << 20
	}
	buf := make([]byte, part)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, err
	}
	if n < part { // the whole object fits one request
		resp, err := s.do(ctx, http.MethodPut, key, nil, buf[:n], map[string]string{"If-None-Match": "*"})
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return 0, s3Err(resp, "put")
		}
		resp.Body.Close()
		return int64(n), nil
	}
	// Multipart.
	resp, err := s.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, nil)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, s3Err(resp, "create multipart upload")
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	err = xml.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&init)
	resp.Body.Close()
	if err != nil || init.UploadID == "" {
		return 0, errors.New("blob: S3 did not return an upload id")
	}
	abort := func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if r, err := s.do(c, http.MethodDelete, key, url.Values{"uploadId": {init.UploadID}}, nil, nil); err == nil {
			r.Body.Close()
		}
	}
	type done struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	var parts []done
	var total int64
	for num := 1; ; num++ {
		if num > 10000 {
			abort()
			return 0, errors.New("blob: object needs more than 10,000 parts; raise PartSize")
		}
		resp, err := s.do(ctx, http.MethodPut, key, url.Values{"partNumber": {strconv.Itoa(num)}, "uploadId": {init.UploadID}}, buf[:n], nil)
		if err != nil {
			abort()
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			abort()
			return 0, s3Err(resp, "upload part")
		}
		resp.Body.Close()
		parts = append(parts, done{num, resp.Header.Get("ETag")})
		total += int64(n)
		if n < part {
			break
		}
		n, err = io.ReadFull(r, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			abort()
			return 0, err
		}
		if n == 0 {
			break
		}
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []done   `xml:"Part"`
	}{Parts: parts})
	resp, err = s.do(ctx, http.MethodPost, key, url.Values{"uploadId": {init.UploadID}}, body, map[string]string{"If-None-Match": "*"})
	if err != nil {
		abort()
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		abort()
		return 0, s3Err(resp, "complete multipart upload")
	}
	// S3 can answer 200 with an error document for this call.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if bytes.Contains(b, []byte("<Error>")) {
		abort()
		return 0, errors.New("blob: S3 complete multipart upload returned an error document")
	}
	return total, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, s3Err(resp, "get")
	}
	return resp.Body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return s3Err(resp, "delete")
	}
	resp.Body.Close()
	return nil
}
