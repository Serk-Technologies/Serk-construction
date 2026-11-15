package blob

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestS3ErrorClassification pins the four fault classes apart against a
// scripted server: a missing bucket, a missing object, a refused credential
// and a failing service must never be reported as one another. (Regression:
// a missing bucket used to surface as "object not found".)
func TestS3ErrorClassification(t *testing.T) {
	doc := func(code string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code></Error>`
	}
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing bucket", 404, doc("NoSuchBucket"), ErrBucketNotFound},
		{"missing object", 404, doc("NoSuchKey"), ErrNotFound},
		{"404 without a document", 404, "", ErrNotFound},
		{"access denied", 403, doc("AccessDenied"), ErrAccessDenied},
		{"bad signature", 403, doc("SignatureDoesNotMatch"), ErrAccessDenied},
		{"unknown access key", 403, doc("InvalidAccessKeyId"), ErrAccessDenied},
		{"unauthorised", 401, "", ErrAccessDenied},
		{"internal error", 500, doc("InternalError"), ErrUnavailable},
		{"unavailable", 503, doc("ServiceUnavailable"), ErrUnavailable},
		{"throttled", 429, doc("SlowDown"), ErrUnavailable},
	}
	all := []error{ErrBucketNotFound, ErrNotFound, ErrAccessDenied, ErrUnavailable, ErrExists, ErrCorrupt}
	key, _ := ObjectKey(tenA, rev, sha)
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(c.body))
			}
		}))
		s := &S3{Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "k", SecretKey: "s"}
		_, err := s.Get(context.Background(), key)
		for _, e := range all {
			if errors.Is(err, e) != (e == c.want) {
				t.Errorf("%s: get returned %v; errors.Is(%v) = %v", c.name, err, e, errors.Is(err, e))
			}
		}
		// A write into a missing bucket says so (the upload path of Run 18).
		if c.want == ErrBucketNotFound {
			if _, err := s.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, ErrBucketNotFound) || errors.Is(err, ErrNotFound) {
				t.Errorf("put into a missing bucket: %v", err)
			}
		}
		srv.Close()
	}
	// CheckBucket: a HEAD carries no document, so the status alone decides.
	for status, want := range map[int]error{200: nil, 404: ErrBucketNotFound, 403: ErrAccessDenied, 401: ErrAccessDenied, 500: ErrUnavailable, 503: ErrUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := (&S3{Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", AccessKey: "k", SecretKey: "s"}).CheckBucket(context.Background())
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("CheckBucket on HTTP %d: %v, want %v", status, err, want)
		}
		srv.Close()
	}
	// No server at all: unavailable, and nothing else.
	dead := &S3{Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "b", AccessKey: "k", SecretKey: "s"}
	if err := dead.CheckBucket(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreachable store: %v", err)
	}
	if _, err := dead.Get(context.Background(), key); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Errorf("unreachable store, get: %v", err)
	}
	// A public bucket is detected.
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer pub.Close()
	if err := (&S3{Endpoint: pub.URL, Bucket: "b"}).VerifyPrivate(context.Background(), key); err == nil || !strings.Contains(err.Error(), "not private") {
		t.Errorf("a bucket that serves anonymous requests passed VerifyPrivate: %v", err)
	}
}

// TestS3FaultClassesAgainstARealServer repeats the classification against a
// real S3-compatible server, in a bucket of its own so it depends on no other
// test and no earlier run.
func TestS3FaultClassesAgainstARealServer(t *testing.T) {
	ep := os.Getenv("SERK_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("SERK_TEST_S3_ENDPOINT is not set: fault classification was not exercised against a server")
	}
	ctx := context.Background()
	good := &S3{Endpoint: ep, Region: "us-east-1", Bucket: "serk-faults-" + hexSHA(random(8))[:16], AccessKey: os.Getenv("SERK_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("SERK_TEST_S3_SECRET_KEY")}
	key, _ := ObjectKey(tenA, rev, hexSHA(random(16)))

	// The bucket does not exist yet.
	if err := good.CheckBucket(ctx); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("check of a missing bucket: %v", err)
	}
	if _, err := good.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, ErrBucketNotFound) || errors.Is(err, ErrNotFound) {
		t.Fatalf("put into a missing bucket: %v", err)
	}
	if _, err := good.Get(ctx, key); !errors.Is(err, ErrBucketNotFound) || errors.Is(err, ErrNotFound) {
		t.Fatalf("get from a missing bucket: %v", err)
	}
	// Provisioned: present, private, and creating it again is not an error.
	if err := good.CreateBucket(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := good.CreateBucket(ctx); err != nil {
		t.Fatalf("create again: %v", err)
	}
	if err := good.CheckBucket(ctx); err != nil {
		t.Fatalf("check after create: %v", err)
	}
	if _, err := good.Get(ctx, key); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing object in an existing bucket: %v", err)
	}
	if _, err := good.Put(ctx, key, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := good.VerifyPrivate(ctx, key); err != nil {
		t.Fatalf("a new bucket must be private: %v", err)
	}
	// Wrong credentials: access denied, for the check, the read and the write.
	for name, bad := range map[string]S3{"wrong secret": {SecretKey: "not-the-secret-not-the-secret", AccessKey: good.AccessKey}, "unknown key": {SecretKey: good.SecretKey, AccessKey: "nobody"}} {
		b := *good
		b.AccessKey, b.SecretKey = bad.AccessKey, bad.SecretKey
		if err := b.CheckBucket(ctx); !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("%s, check: %v", name, err)
		}
		if _, err := b.Get(ctx, key); !errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrNotFound) {
			t.Fatalf("%s, get: %v", name, err)
		}
		k2, _ := ObjectKey(tenA, rev, hexSHA(random(16)))
		if _, err := b.Put(ctx, k2, strings.NewReader("x")); !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("%s, put: %v", name, err)
		}
	}
	if err := good.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}
