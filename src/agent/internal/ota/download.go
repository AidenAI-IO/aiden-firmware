package ota

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

func DownloadFile(ctx context.Context, url string, dst string, expectedSize int64) error {
	return DownloadFileWithToken(ctx, url, dst, expectedSize, "")
}

func DownloadFileWithToken(ctx context.Context, url string, dst string, expectedSize int64, bearerToken string) error {
	part := dst + ".part"
	resumeAt := int64(0)
	if info, err := os.Stat(part); err == nil {
		resumeAt = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}
	if expectedSize >= 0 {
		if resumeAt == expectedSize {
			if err := os.Rename(part, dst); err != nil {
				return err
			}
			return fsyncDirFor(dst)
		}
		if resumeAt > expectedSize {
			return fmt.Errorf("download size %d exceeds expected %d", resumeAt, expectedSize)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if resumeAt > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeAt))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumeAt > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		resumeAt = 0
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	copyErr := error(nil)
	var copied int64
	if expectedSize >= 0 {
		remaining := expectedSize - resumeAt
		copied, copyErr = io.Copy(f, io.LimitReader(resp.Body, remaining+1))
		if copyErr == nil && copied > remaining {
			copyErr = fmt.Errorf("download size exceeds expected %d", expectedSize)
		}
	} else {
		copied, copyErr = io.Copy(f, resp.Body)
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}

	info, err := os.Stat(part)
	if err != nil {
		return err
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return fmt.Errorf("download size %d, want %d", info.Size(), expectedSize)
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	return fsyncDirFor(dst)
}
