package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/blob"
)

// TestStorageFaultsAreNotReportedAsNotFound: a missing bucket, refused storage
// credentials and an unreachable store are answered as a retryable 503 with no
// internal detail; only a genuinely missing record is a 404.
func TestStorageFaultsAreNotReportedAsNotFound(t *testing.T) {
	s := &Server{cfg: Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	for name, c := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"missing bucket":   {fmt.Errorf("kernel: storing the file: %w", fmt.Errorf("%w (S3 put: HTTP 404 NoSuchBucket)", blob.ErrBucketNotFound)), 503, "storage_unavailable"},
		"access denied":    {fmt.Errorf("kernel: storing the file: %w", fmt.Errorf("%w (S3 put: HTTP 403 AccessDenied)", blob.ErrAccessDenied)), 503, "storage_unavailable"},
		"store down":       {fmt.Errorf("kernel: reading the file: %w", fmt.Errorf("%w (dial tcp 10.0.0.9:9000: refused)", blob.ErrUnavailable)), 503, "storage_unavailable"},
		"key not supplied": {fmt.Errorf("kernel: reading the file: %w", fmt.Errorf("%w: object needs key %q", blob.ErrKeyUnavailable, "d1")), 503, "storage_unavailable"},
		"missing record":   {kernel.ErrNotFound, 404, "not_found"},
		"missing object?":  {fmt.Errorf("kernel: reading the file: %w", blob.ErrNotFound), 500, ""},
	} {
		w := httptest.NewRecorder()
		s.fail(w, c.err)
		body := w.Body.String()
		if w.Code != c.status || (c.code != "" && !strings.Contains(body, c.code)) {
			t.Errorf("%s: HTTP %d %s", name, w.Code, body)
		}
		for _, leak := range []string{"NoSuchBucket", "AccessDenied", "10.0.0.9", "S3 put"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: response leaks %q: %s", name, leak, body)
			}
		}
		if c.status == 503 && w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After", name)
		}
	}
}
