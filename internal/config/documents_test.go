package config

import "testing"

const docKey = "Kq3vT9xLm2Zp8Rw5Ye7Nb4Hc6Jd1Fg0Sa-document"

// Document storage and scanning configuration (ADR 0019): production takes an
// https object store and a ClamAV address, and refuses the development store,
// a plaintext endpoint, storage or scanning without the document key, and a
// document key that repeats another secret.
func TestDocumentStorageConfig(t *testing.T) {
	load := func(f func(m map[string]string)) (Config, error) {
		m := prodEnv()
		f(m)
		return Load(src(m, nil))
	}
	c, err := load(func(m map[string]string) {
		m["SERK_DOCUMENT_KEY"] = docKey
		m["SERK_S3_ENDPOINT"], m["SERK_S3_BUCKET"], m["SERK_S3_ACCESS_KEY"], m["SERK_S3_SECRET_KEY"] = "https://s3.example.net", "serk-docs", "AKIAEXAMPLE", "s3-secret-value"
		m["SERK_CLAMD_ADDR"], m["SERK_DOCUMENT_MAX_MB"] = "clamav:3310", "2048"
	})
	if err != nil {
		t.Fatalf("hardened document configuration refused: %v", err)
	}
	if kind, scanner := c.DocumentStorage(); kind != "s3" || !scanner || c.DocumentMaxMB != 2048 {
		t.Fatalf("storage %s scanner %v max %d", kind, scanner, c.DocumentMaxMB)
	}
	if c, err := load(func(m map[string]string) {}); err != nil {
		t.Fatalf("configuration without document storage refused: %v", err)
	} else if kind, scanner := c.DocumentStorage(); kind != "postgres" || scanner {
		t.Fatalf("default storage %s scanner %v", kind, scanner)
	}
	s3 := func(m map[string]string) {
		m["SERK_DOCUMENT_KEY"] = docKey
		m["SERK_S3_ENDPOINT"], m["SERK_S3_BUCKET"], m["SERK_S3_ACCESS_KEY"], m["SERK_S3_SECRET_KEY"] = "https://s3.example.net", "serk-docs", "AKIAEXAMPLE", "s3-secret-value"
	}
	for name, f := range map[string]func(m map[string]string){
		"development store in production": func(m map[string]string) { m["SERK_DOCUMENT_KEY"], m["SERK_BLOB_DIR"] = docKey, "/var/lib/serk/blobs" },
		"plaintext object store":          func(m map[string]string) { s3(m); m["SERK_S3_ENDPOINT"] = "http://s3.example.net" },
		"object store without the key":    func(m map[string]string) { s3(m); delete(m, "SERK_DOCUMENT_KEY") },
		"object store without a bucket":   func(m map[string]string) { s3(m); delete(m, "SERK_S3_BUCKET") },
		"object store without a secret":   func(m map[string]string) { s3(m); delete(m, "SERK_S3_SECRET_KEY") },
		"scanner without the key":         func(m map[string]string) { m["SERK_CLAMD_ADDR"] = "clamav:3310" },
		"scanner address without a port":  func(m map[string]string) { m["SERK_DOCUMENT_KEY"], m["SERK_CLAMD_ADDR"] = docKey, "clamav" },
		"relative scanner socket": func(m map[string]string) {
			m["SERK_DOCUMENT_KEY"], m["SERK_CLAMD_ADDR"] = docKey, "unix:run/clamd.sock"
		},
		"weak document key":             func(m map[string]string) { m["SERK_DOCUMENT_KEY"] = "short" },
		"document key repeats a secret": func(m map[string]string) { m["SERK_DOCUMENT_KEY"] = m["SERK_TENANT_DB_SECRET"] },
		"nonsense file limit":           func(m map[string]string) { m["SERK_DOCUMENT_MAX_MB"] = "lots" },
	} {
		if _, err := load(f); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
