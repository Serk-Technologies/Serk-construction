// Command s3provision prepares and proves the object-store bucket that the
// integration tests (and any environment set up the same way) depend on.
//
// It authenticates with the given credentials, creates the bucket if it is
// absent, and then verifies, failing with a non-zero status on the first thing
// that does not hold:
//
//   - the bucket exists and accepts the credentials;
//   - an object can be written, read back identically and deleted;
//   - requests without credentials are refused (the bucket is private);
//   - requests with a wrong secret are refused.
//
// Configuration comes from the environment so no secret appears on a command
// line: S3_ENDPOINT, S3_REGION (default us-east-1), S3_BUCKET, S3_ACCESS_KEY,
// S3_SECRET_KEY. Pass -create=false to verify only.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/serk-group/serk-construction/pkg/blob"
)

func main() {
	create := flag.Bool("create", true, "create the bucket when it does not exist")
	flag.Parse()
	if err := run(*create); err != nil {
		fmt.Fprintln(os.Stderr, "s3provision: FAIL:", err)
		os.Exit(1)
	}
}

func run(create bool) error {
	s := &blob.S3{Endpoint: os.Getenv("S3_ENDPOINT"), Region: os.Getenv("S3_REGION"), Bucket: os.Getenv("S3_BUCKET"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY")}
	if s.Region == "" {
		s.Region = "us-east-1"
	}
	if s.Endpoint == "" || s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
		return errors.New("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY and S3_SECRET_KEY are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err := s.CheckBucket(ctx)
	switch {
	case err == nil:
		fmt.Printf("bucket %q: exists\n", s.Bucket)
	case errors.Is(err, blob.ErrBucketNotFound) && create:
		if err := s.CreateBucket(ctx); err != nil {
			return fmt.Errorf("create bucket: %w", err)
		}
		if err := s.CheckBucket(ctx); err != nil {
			return fmt.Errorf("bucket not usable after creation: %w", err)
		}
		fmt.Printf("bucket %q: created\n", s.Bucket)
	default:
		return err
	}

	// Write, read back, compare.
	payload := make([]byte, 4096)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	key, err := blob.ObjectKey("00000000-0000-4000-8000-000000000000", "00000000-0000-4000-8000-000000000001", hex.EncodeToString(sum[:]))
	if err != nil {
		return err
	}
	if _, err := s.Put(ctx, key, bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	defer func() { _ = s.Delete(context.Background(), key) }()
	r, err := s.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read probe: %w", err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(got, payload) {
		return fmt.Errorf("probe read back differently (%v)", err)
	}
	fmt.Println("authenticated write and read: ok")

	if err := s.VerifyPrivate(ctx, key); err != nil {
		return err
	}
	fmt.Println("anonymous list and read: refused (private)")

	wrong := *s
	wrong.SecretKey = "wrong-" + hex.EncodeToString(sum[:8])
	if err := wrong.CheckBucket(ctx); !errors.Is(err, blob.ErrAccessDenied) {
		return fmt.Errorf("a wrong secret was not refused: %v", err)
	}
	fmt.Println("wrong secret: refused")
	if err := s.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete probe: %w", err)
	}
	fmt.Println("s3provision: PASS")
	return nil
}
