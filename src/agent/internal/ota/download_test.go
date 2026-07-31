package ota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDownloadResumesPartFileWithRange(t *testing.T) {
	content := "hello resumed world"
	var rangeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader = r.Header.Get("Range")
		if rangeHeader != "bytes=6-" {
			t.Fatalf("Range = %q", rangeHeader)
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(content[6:]))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	if err := os.WriteFile(dst+".part", []byte(content[:6]), 0o644); err != nil {
		t.Fatalf("WriteFile(part) error = %v", err)
	}
	if err := DownloadFile(context.Background(), server.URL, dst, int64(len(content))); err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile(dst) error = %v", err)
	}
	if string(got) != content {
		t.Fatalf("downloaded = %q", got)
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part file still exists, stat err = %v", err)
	}
}

func TestDownloadPromotesCompletePartFileWithoutNetwork(t *testing.T) {
	content := "already complete"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be contacted for complete part file")
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	if err := os.WriteFile(dst+".part", []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(part) error = %v", err)
	}
	if err := DownloadFile(context.Background(), server.URL, dst, int64(len(content))); err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile(dst) error = %v", err)
	}
	if string(got) != content {
		t.Fatalf("downloaded = %q", got)
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Fatalf("part file still exists, stat err = %v", err)
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	content := "fresh full body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Fatal("missing Range for existing part")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	if err := os.WriteFile(dst+".part", []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(part) error = %v", err)
	}
	if err := DownloadFile(context.Background(), server.URL, dst, int64(len(content))); err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile(dst) error = %v", err)
	}
	if string(got) != content {
		t.Fatalf("downloaded = %q", got)
	}
}

func TestDownloadChecksFinalSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()

	err := DownloadFile(context.Background(), server.URL, filepath.Join(t.TempDir(), "image.img"), 10)
	if err == nil || !strings.Contains(err.Error(), "download size") {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadFailsWhenResponseExceedsExpectedSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("123456"))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	err := DownloadFile(context.Background(), server.URL, dst, 5)
	if err == nil || !strings.Contains(err.Error(), "download size") {
		t.Fatalf("error = %v, want download size failure", err)
	}
	if info, statErr := os.Stat(dst + ".part"); statErr != nil {
		t.Fatalf("stat part error = %v", statErr)
	} else if info.Size() > 6 {
		t.Fatalf("part size = %d, want bounded write", info.Size())
	}
}

func TestDownloadRemovesPartialWhenCloseReportsNoSpaceAfterCopyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12"))
	}))
	defer server.Close()

	originalClose := closeDownloadFile
	closeDownloadFile = func(f *os.File) error {
		if err := f.Close(); err != nil {
			return err
		}
		return syscall.ENOSPC
	}
	t.Cleanup(func() { closeDownloadFile = originalClose })

	dst := filepath.Join(t.TempDir(), "image.img")
	err := DownloadFile(context.Background(), server.URL, dst, 1)
	if err == nil || !strings.Contains(err.Error(), "download size exceeds expected") {
		t.Fatalf("error = %v, want original copy-size failure", err)
	}
	if _, statErr := os.Stat(dst + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("part file remains after close-time ENOSPC: %v", statErr)
	}
}

func TestDownloadFailsWhenResumedResponseExceedsExpectedSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=3-" {
			t.Fatalf("Range = %q", r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("4567"))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	if err := os.WriteFile(dst+".part", []byte("123"), 0o644); err != nil {
		t.Fatalf("WriteFile(part) error = %v", err)
	}
	err := DownloadFile(context.Background(), server.URL, dst, 5)
	if err == nil || !strings.Contains(err.Error(), "download size") {
		t.Fatalf("error = %v, want download size failure", err)
	}
	if info, statErr := os.Stat(dst + ".part"); statErr != nil {
		t.Fatalf("stat part error = %v", statErr)
	} else if info.Size() > 6 {
		t.Fatalf("part size = %d, want bounded write", info.Size())
	}
}

func TestDownloadReportsProgressAndCompletion(t *testing.T) {
	content := "progress body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "13")
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()

	dst := filepath.Join(t.TempDir(), "image.img")
	var reports []DownloadProgress
	err := DownloadFileWithOptions(context.Background(), server.URL, dst, int64(len(content)), DownloadOptions{
		Progress: func(progress DownloadProgress) {
			reports = append(reports, progress)
		},
	})
	if err != nil {
		t.Fatalf("DownloadFileWithOptions() error = %v", err)
	}
	if len(reports) == 0 {
		t.Fatalf("no progress reports")
	}
	last := reports[len(reports)-1]
	if !last.Complete {
		t.Fatalf("last progress report = %+v, want Complete", last)
	}
	if last.Bytes != int64(len(content)) || last.Total != int64(len(content)) {
		t.Fatalf("last progress report = %+v, want full byte count", last)
	}
	if last.Path != dst || last.URL != server.URL {
		t.Fatalf("last progress report path/url = %q/%q", last.Path, last.URL)
	}
}
