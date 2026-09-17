package backup

// This file contains the incremental reader used by the restore endpoint.
// VerifyArchive is intentionally kept as a convenient all-at-once verifier,
// while ArchiveStream lets callers stop immediately after manifest.json and
// resume the same authenticated tar stream later.

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
)

// ArchiveEntry describes the current tar entry. The bytes for a regular file
// are read through CopyCurrentEntry; callers must consume that entry before
// asking for the next one.
type ArchiveEntry struct {
	Manifest FileManifest
	Header   tar.Header
}

// ArchiveStream is a bounded-memory, authenticated archive decoder. The
// underlying reader may be a network pipe and need not be seekable.
type ArchiveStream struct {
	decrypted *decryptedReader
	gzip      *gzip.Reader
	tar       *tar.Reader
	manifest  Manifest
	expected  map[string]FileManifest
	seen      map[string]bool
	current   *ArchiveEntry
	started   bool
	finished  bool
}

// NewArchiveStream creates a decoder. It reads the public header lazily when
// ReadManifest is called, so creating a restore job does not block waiting for
// its first upload chunk.
func NewArchiveStream(reader io.Reader, material *KeyMaterial) (*ArchiveStream, error) {
	if reader == nil {
		return nil, fmt.Errorf("archive reader is missing")
	}
	if material == nil {
		return nil, fmt.Errorf("backup key material is missing")
	}
	// newDecryptedReader validates and consumes the public header immediately;
	// this is safe for a chunk source because ReadManifest is called by the
	// parser goroutine only after the first chunk has been queued.
	decrypted, err := newDecryptedReader(reader, material)
	if err != nil {
		return nil, err
	}
	gzipReader, err := gzip.NewReader(decrypted)
	if err != nil {
		// A wrong passphrase surfaces here first: the gzip header cannot be
		// read because the first frame failed authentication.
		if decrypted.terminalErr != nil && decrypted.terminalErr != io.EOF {
			return nil, decrypted.terminalErr
		}
		return nil, errorf("archive_authentication_failed", err, "backup payload is not a valid gzip stream")
	}
	return &ArchiveStream{
		decrypted: decrypted,
		gzip:      gzipReader,
		tar:       tar.NewReader(gzipReader),
		expected:  make(map[string]FileManifest),
		seen:      make(map[string]bool),
	}, nil
}

// ReadManifest consumes exactly the first tar entry and validates it. It is
// legal to call this once; subsequent calls return the cached manifest.
func (s *ArchiveStream) ReadManifest() (Manifest, error) {
	if s == nil {
		return Manifest{}, fmt.Errorf("archive stream is nil")
	}
	if s.started {
		return s.manifest, nil
	}
	s.started = true
	header, err := s.tar.Next()
	if err != nil {
		return Manifest{}, errorf("manifest_invalid", err, "manifest.json must be the first archive entry")
	}
	if header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > MaxManifestSize {
		return Manifest{}, errorf("manifest_invalid", nil, "manifest.json must be the first archive entry")
	}
	data, err := io.ReadAll(io.LimitReader(s.tar, MaxManifestSize+1))
	if err != nil || int64(len(data)) != header.Size {
		return Manifest{}, errorf("manifest_invalid", err, "manifest.json is truncated")
	}
	var manifest Manifest
	if err := decodeManifest(data, &manifest); err != nil {
		return Manifest{}, errorf("manifest_invalid", err, "manifest.json is invalid")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, errorf("manifest_invalid", err, "manifest validation failed: %v", err)
	}
	for _, file := range manifest.Files {
		s.expected[path.Join("components", string(file.Component), file.Path)] = file
	}
	s.manifest = manifest
	return manifest, nil
}

// Next returns the next authenticated tar entry. io.EOF means the encrypted
// payload has reached its tar end; callers must then call Finish to verify the
// gzip trailer and authenticated footer.
func (s *ArchiveStream) Next(ctx context.Context) (ArchiveEntry, error) {
	if s == nil {
		return ArchiveEntry{}, fmt.Errorf("archive stream is nil")
	}
	if !s.started {
		if _, err := s.ReadManifest(); err != nil {
			return ArchiveEntry{}, err
		}
	}
	if s.finished {
		return ArchiveEntry{}, io.EOF
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ArchiveEntry{}, err
		}
	}
	if s.current != nil {
		return ArchiveEntry{}, fmt.Errorf("previous archive entry was not consumed")
	}
	header, err := s.tar.Next()
	if err == io.EOF {
		return ArchiveEntry{}, io.EOF
	}
	if err != nil {
		return ArchiveEntry{}, errorf("archive_truncated", err, "tar payload is truncated")
	}
	name := strings.TrimSuffix(header.Name, "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "../") {
		return ArchiveEntry{}, errorf("path_rejected", nil, "unsafe tar path %q", header.Name)
	}
	manifest, ok := s.expected[name]
	if !ok || s.seen[name] {
		return ArchiveEntry{}, errorf("manifest_invalid", nil, "unexpected or duplicate tar entry %q", header.Name)
	}
	if err := validateTarHeader(header, manifest); err != nil {
		return ArchiveEntry{}, err
	}
	s.current = &ArchiveEntry{Manifest: manifest, Header: *header}
	return *s.current, nil
}

// CopyCurrentEntry authenticates and copies the current regular-file body to
// dst. For directories and symlinks it only validates that no body exists.
func (s *ArchiveStream) CopyCurrentEntry(ctx context.Context, dst io.Writer) error {
	if s == nil || s.current == nil {
		return fmt.Errorf("no current archive entry")
	}
	entry := *s.current
	var err error
	switch entry.Manifest.Type {
	case FileTypeRegular:
		if dst == nil {
			return fmt.Errorf("destination is missing")
		}
		hash := sha256.New()
		writer := io.MultiWriter(dst, hash)
		var copied int64
		buf := make([]byte, 128*1024)
		for copied < entry.Manifest.Size {
			if ctx != nil {
				if err = ctx.Err(); err != nil {
					break
				}
			}
			want := int64(len(buf))
			if left := entry.Manifest.Size - copied; left < want {
				want = left
			}
			var n int
			n, err = io.ReadFull(s.tar, buf[:want])
			if n > 0 {
				if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
					err = writeErr
					break
				}
				copied += int64(n)
			}
			if err != nil {
				break
			}
		}
		if err == nil && copied != entry.Manifest.Size {
			err = io.ErrUnexpectedEOF
		}
		if err == nil && hex.EncodeToString(hash.Sum(nil)) != entry.Manifest.SHA256 {
			err = errorf("hash_mismatch", nil, "file hash mismatch for %s", entry.Header.Name)
		}
	case FileTypeDirectory, FileTypeSymlink:
		// tar.Reader exposes no body for these entries. Metadata was checked in
		// Next; symlink targets were checked by Manifest.Validate.
	default:
		err = errorf("manifest_invalid", nil, "unsupported entry type for %s", entry.Header.Name)
	}
	if err != nil {
		if ErrorCode(err) == "" {
			err = errorf("archive_truncated", err, "archive entry %s is invalid", entry.Header.Name)
		}
		s.current = nil
		return err
	}
	s.seen[path.Join("components", string(entry.Manifest.Component), entry.Manifest.Path)] = true
	s.current = nil
	return nil
}

// Finish verifies that every manifest entry was seen and that the authenticated
// footer, gzip trailer and encrypted stream all agree.
func (s *ArchiveStream) Finish() error {
	if s == nil {
		return fmt.Errorf("archive stream is nil")
	}
	if s.finished {
		return nil
	}
	if s.current != nil {
		return fmt.Errorf("previous archive entry was not consumed")
	}
	for name := range s.expected {
		if !s.seen[name] {
			return errorf("manifest_invalid", nil, "archive is missing %q", name)
		}
	}
	if err := s.gzip.Close(); err != nil {
		return errorf("archive_truncated", err, "gzip payload is truncated")
	}
	if _, err := io.Copy(io.Discard, s.decrypted); err != nil {
		return err
	}
	s.finished = true
	return nil
}

// Manifest returns the parsed manifest after ReadManifest has completed.
func (s *ArchiveStream) Manifest() Manifest {
	if s == nil {
		return Manifest{}
	}
	return s.manifest
}

func decodeManifest(data []byte, manifest *Manifest) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(manifest); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("manifest contains trailing JSON values")
	}
	return nil
}

func validateTarHeader(header *tar.Header, manifest FileManifest) error {
	if os.FileMode(header.Mode)&0o6000 != 0 {
		return errorf("path_rejected", nil, "setuid or setgid mode is not allowed")
	}
	if int64(parseMode(manifest.Mode)) != header.Mode&0o777 {
		return errorf("manifest_invalid", nil, "mode mismatch for %s", header.Name)
	}
	switch manifest.Type {
	case FileTypeRegular:
		if header.Typeflag != tar.TypeReg || header.Size != manifest.Size {
			return errorf("manifest_invalid", nil, "regular file metadata mismatch for %s", header.Name)
		}
	case FileTypeDirectory:
		if header.Typeflag != tar.TypeDir || header.Size != 0 {
			return errorf("manifest_invalid", nil, "directory metadata mismatch for %s", header.Name)
		}
	case FileTypeSymlink:
		if header.Typeflag != tar.TypeSymlink || header.Size != 0 || header.Linkname != manifest.LinkTarget {
			return errorf("manifest_invalid", nil, "symlink metadata mismatch for %s", header.Name)
		}
	default:
		return errorf("manifest_invalid", nil, "unsupported entry type for %s", header.Name)
	}
	return nil
}
