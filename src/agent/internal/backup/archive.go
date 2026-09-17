package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

type Progress struct {
	Component      ComponentID
	FilesProcessed int
	BytesRead      int64
}

type ProgressFunc func(Progress)

func WriteArchive(ctx context.Context, writer io.Writer, plan *Plan, material *KeyMaterial, progress ProgressFunc) error {
	if plan == nil {
		return fmt.Errorf("backup plan is missing")
	}
	manifestData, err := plan.Manifest.MarshalCanonical()
	if err != nil {
		return err
	}
	if len(manifestData) > MaxManifestSize {
		return fmt.Errorf("backup manifest exceeds %d bytes", MaxManifestSize)
	}
	encrypted, err := newEncryptedWriter(writer, material)
	if err != nil {
		return err
	}
	gzipWriter, err := gzip.NewWriterLevel(encrypted, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tarWriter := tar.NewWriter(gzipWriter)
	failed := true
	defer func() {
		if failed {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
		}
	}()
	createdAt := plan.Manifest.CreatedAt
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o600, Size: int64(len(manifestData)),
		ModTime: createdAt, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
	}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(manifestData); err != nil {
		return err
	}

	state := Progress{}
	for _, entry := range plan.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		state.Component = entry.Manifest.Component
		name := path.Join("components", string(entry.Manifest.Component), entry.Manifest.Path)
		header := &tar.Header{
			Name: name, Mode: int64(parseMode(entry.Manifest.Mode)),
			ModTime: createdAt, Format: tar.FormatPAX,
		}
		switch entry.Manifest.Type {
		case FileTypeDirectory:
			if err := verifyNonRegularEntry(entry); err != nil {
				return err
			}
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		case FileTypeSymlink:
			if err := verifyNonRegularEntry(entry); err != nil {
				return err
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.Manifest.LinkTarget
		case FileTypeRegular:
			header.Typeflag = tar.TypeReg
			header.Size = entry.Manifest.Size
		default:
			return fmt.Errorf("unsupported planned file type %q", entry.Manifest.Type)
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.Manifest.Type == FileTypeRegular {
			if err := writePlannedRegular(ctx, tarWriter, entry, &state, progress); err != nil {
				return err
			}
		}
		state.FilesProcessed++
		if progress != nil {
			progress(state)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}

func writePlannedRegular(ctx context.Context, writer io.Writer, entry PlannedEntry, state *Progress, progress ProgressFunc) error {
	if entry.Generated != nil {
		if int64(len(entry.Generated)) != entry.Manifest.Size {
			return fmt.Errorf("generated file size changed for %s/%s", entry.Manifest.Component, entry.Manifest.Path)
		}
		if _, err := writer.Write(entry.Generated); err != nil {
			return err
		}
		state.BytesRead += int64(len(entry.Generated))
		return nil
	}
	fd, err := unix.Open(entry.SourcePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", entry.SourcePath, err)
	}
	file := os.NewFile(uintptr(fd), entry.SourcePath)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !sameFingerprint(entry.fingerprint, fingerprint(info)) {
		return fmt.Errorf("source changed after planning: %s", entry.SourcePath)
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	remaining := entry.Manifest.Size
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buffer))
		if remaining < want {
			want = remaining
		}
		count, readErr := io.ReadFull(file, buffer[:want])
		if count > 0 {
			if _, err := writer.Write(buffer[:count]); err != nil {
				return err
			}
			_, _ = hash.Write(buffer[:count])
			remaining -= int64(count)
			state.BytesRead += int64(count)
			if progress != nil {
				progress(*state)
			}
		}
		if readErr != nil {
			return fmt.Errorf("read %s: %w", entry.SourcePath, readErr)
		}
	}
	var extra [1]byte
	if count, err := file.Read(extra[:]); count != 0 || (err != nil && err != io.EOF) {
		return fmt.Errorf("source size changed while archiving: %s", entry.SourcePath)
	}
	after, err := os.Lstat(entry.SourcePath)
	if err != nil || !sameFingerprint(entry.fingerprint, fingerprint(after)) {
		return fmt.Errorf("source changed while archiving: %s", entry.SourcePath)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != entry.Manifest.SHA256 {
		return fmt.Errorf("source digest changed while archiving: %s", entry.SourcePath)
	}
	return nil
}

func verifyNonRegularEntry(entry PlannedEntry) error {
	info, err := os.Lstat(entry.SourcePath)
	if err != nil {
		return err
	}
	if !sameFingerprint(entry.fingerprint, fingerprint(info)) {
		return fmt.Errorf("source changed after planning: %s", entry.SourcePath)
	}
	if entry.Manifest.Type == FileTypeDirectory && !info.IsDir() {
		return fmt.Errorf("source is no longer a directory: %s", entry.SourcePath)
	}
	if entry.Manifest.Type == FileTypeSymlink {
		target, err := os.Readlink(entry.SourcePath)
		if err != nil || filepathToSlash(target) != entry.Manifest.LinkTarget {
			return fmt.Errorf("source symlink changed after planning: %s", entry.SourcePath)
		}
	}
	return nil
}

func filepathToSlash(value string) string { return strings.ReplaceAll(value, "\\", "/") }

func parseMode(value string) os.FileMode {
	var mode uint32
	_, _ = fmt.Sscanf(value, "%o", &mode)
	return os.FileMode(mode & 0o777)
}

type VerifyResult struct {
	Header   PublicHeader
	Manifest Manifest
	Bytes    int64
}

func VerifyArchive(ctx context.Context, reader io.Reader, passphrase []byte) (VerifyResult, error) {
	var result VerifyResult
	header, headerData, err := ReadPublicHeader(reader)
	if err != nil {
		return result, err
	}
	material, err := DeriveKeyMaterial(header, passphrase)
	if err != nil {
		return result, err
	}
	defer material.Destroy()
	result.Header = header
	// newDecryptedReader validates the public header again. Reconstruct the
	// prefix so callers can use one streaming reader without seeking.
	prefix := &prefixReader{prefix: publicHeaderPrefix(headerData), source: reader}
	decrypted, err := newDecryptedReader(prefix, material)
	if err != nil {
		return result, err
	}
	gzipReader, err := gzip.NewReader(decrypted)
	if err != nil {
		// A wrong passphrase surfaces here first: the gzip header cannot be
		// read because the first frame failed authentication.
		if decrypted.terminalErr != nil && decrypted.terminalErr != io.EOF {
			return result, decrypted.terminalErr
		}
		return result, errorf("archive_authentication_failed", err, "backup payload is not a valid gzip stream")
	}
	tarReader := tar.NewReader(gzipReader)
	first, err := tarReader.Next()
	if err != nil || first.Name != "manifest.json" || first.Typeflag != tar.TypeReg || first.Size < 0 || first.Size > MaxManifestSize {
		return result, errorf("manifest_invalid", err, "manifest.json must be the first archive entry")
	}
	manifestData, err := io.ReadAll(io.LimitReader(tarReader, MaxManifestSize+1))
	if err != nil || int64(len(manifestData)) != first.Size {
		return result, errorf("manifest_invalid", err, "manifest.json is truncated")
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Manifest); err != nil {
		return result, errorf("manifest_invalid", err, "manifest.json is invalid")
	}
	if err := result.Manifest.Validate(); err != nil {
		return result, errorf("manifest_invalid", err, "manifest validation failed: %v", err)
	}
	expected := make(map[string]FileManifest, len(result.Manifest.Files))
	for _, file := range result.Manifest.Files {
		expected[path.Join("components", string(file.Component), file.Path)] = file
	}
	seen := make(map[string]bool, len(expected))
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, errorf("archive_truncated", err, "tar payload is truncated")
		}
		name := strings.TrimSuffix(header.Name, "/")
		manifest, ok := expected[name]
		if !ok || seen[name] {
			return result, errorf("manifest_invalid", nil, "unexpected or duplicate tar entry %q", header.Name)
		}
		seen[name] = true
		if err := verifyTarEntry(tarReader, header, manifest); err != nil {
			return result, err
		}
		result.Bytes += manifest.Size
	}
	if err := gzipReader.Close(); err != nil {
		return result, errorf("archive_truncated", err, "gzip payload is truncated")
	}
	if _, err := io.Copy(io.Discard, decrypted); err != nil {
		return result, err
	}
	for name := range expected {
		if !seen[name] {
			return result, errorf("manifest_invalid", nil, "archive is missing %q", name)
		}
	}
	return result, nil
}

func verifyTarEntry(reader io.Reader, header *tar.Header, manifest FileManifest) error {
	if header.Name == "" || strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "../") {
		return errorf("path_rejected", nil, "unsafe tar path %q", header.Name)
	}
	if err := validateTarHeader(header, manifest); err != nil {
		return err
	}
	switch manifest.Type {
	case FileTypeRegular:
		hash := sha256.New()
		count, err := io.Copy(hash, reader)
		if err != nil || count != manifest.Size {
			return errorf("archive_truncated", err, "file %s is truncated", header.Name)
		}
		if hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
			return errorf("hash_mismatch", nil, "file hash mismatch for %s", header.Name)
		}
	case FileTypeDirectory:
	case FileTypeSymlink:
	default:
		return errorf("manifest_invalid", nil, "unsupported entry type for %s", header.Name)
	}
	return nil
}

// prefixReader replays a parsed public header before forwarding the remaining
// archive stream. This keeps public-header inspection and full verification
// compatible with non-seekable HTTP and file streams.
type prefixReader struct {
	prefix []byte
	source io.Reader
}

func (r *prefixReader) Read(target []byte) (int, error) {
	if len(r.prefix) > 0 {
		count := copy(target, r.prefix)
		r.prefix = r.prefix[count:]
		return count, nil
	}
	return r.source.Read(target)
}
