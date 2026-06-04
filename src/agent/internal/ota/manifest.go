package ota

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	Channel       string            `json:"channel"`
	Version       string            `json:"version"`
	BuildTime     string            `json:"build_time"`
	Parts         []ManifestPart    `json:"parts"`
	Signature     ManifestSignature `json:"signature"`
}

type ManifestSignature struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value,omitempty"`
}

type ManifestPart struct {
	Name               string         `json:"name"`
	Asset              *ManifestAsset `json:"asset,omitempty"`
	AssetA             *ManifestAsset `json:"asset_a,omitempty"`
	AssetB             *ManifestAsset `json:"asset_b,omitempty"`
	RequiresPartitions []string       `json:"requires_partitions,omitempty"`
}

type ManifestAsset struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var (
	manifestChannelRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	manifestVersionRE = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
	assetNameRE       = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,/-]+$`)
)

func CanonicalManifestJSON(manifest Manifest) ([]byte, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return CanonicalManifestJSONBytes(encoded)
}

func CanonicalManifestJSONBytes(encoded []byte) ([]byte, error) {
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	if sig, ok := value["signature"].(map[string]any); ok {
		delete(sig, "value")
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("schema_version %d, want 1", m.SchemaVersion)
	}
	if !manifestChannelRE.MatchString(m.Channel) {
		return fmt.Errorf("invalid channel %q", m.Channel)
	}
	if !manifestVersionRE.MatchString(m.Version) {
		return fmt.Errorf("invalid version %q", m.Version)
	}
	if _, err := time.Parse(time.RFC3339, m.BuildTime); err != nil {
		return fmt.Errorf("invalid build_time %q: %w", m.BuildTime, err)
	}
	if len(m.Parts) == 0 {
		return errors.New("manifest has no parts")
	}

	seen := map[string]bool{}
	for _, part := range m.Parts {
		if part.Name != "boot" && part.Name != "oem" && part.Name != "rootfs" {
			return fmt.Errorf("unknown part %q", part.Name)
		}
		if seen[part.Name] {
			return fmt.Errorf("duplicate part %q", part.Name)
		}
		seen[part.Name] = true

		if part.Name == "boot" {
			if part.Asset != nil || part.AssetA == nil || part.AssetB == nil {
				return errors.New("boot requires asset_a and asset_b and cannot use neutral asset")
			}
			if err := validateManifestAsset("boot.asset_a", *part.AssetA, "boot_a.img"); err != nil {
				return err
			}
			if err := validateManifestAsset("boot.asset_b", *part.AssetB, "boot_b.img"); err != nil {
				return err
			}
			continue
		}

		hasNeutral := part.Asset != nil
		hasSlotSpecific := part.AssetA != nil || part.AssetB != nil
		if hasNeutral == hasSlotSpecific {
			return fmt.Errorf("%s requires either neutral asset or both slot-specific assets", part.Name)
		}
		if hasNeutral {
			if err := validateManifestAsset(part.Name+".asset", *part.Asset, part.Name+".img"); err != nil {
				return err
			}
			continue
		}
		if part.AssetA == nil || part.AssetB == nil {
			return fmt.Errorf("%s slot-specific form requires asset_a and asset_b", part.Name)
		}
		if err := validateManifestAsset(part.Name+".asset_a", *part.AssetA, part.Name+"_a.img"); err != nil {
			return err
		}
		if err := validateManifestAsset(part.Name+".asset_b", *part.AssetB, part.Name+"_b.img"); err != nil {
			return err
		}
	}
	return nil
}

func validateManifestAsset(field string, asset ManifestAsset, expectedName string) error {
	if asset.Name == "" || !assetNameRE.MatchString(asset.Name) || strings.Contains(asset.Name, "..") || strings.HasPrefix(asset.Name, "/") {
		return fmt.Errorf("%s has invalid name %q", field, asset.Name)
	}
	if asset.Name != expectedName {
		return fmt.Errorf("%s name %q, want %q", field, asset.Name, expectedName)
	}
	if asset.URL != "" {
		if !strings.HasPrefix(asset.URL, "http://") && !strings.HasPrefix(asset.URL, "https://") {
			return fmt.Errorf("%s has invalid URL scheme (must be http or https): %q", field, asset.URL)
		}
		if strings.Contains(asset.URL, " ") {
			return fmt.Errorf("%s URL contains whitespace: %q", field, asset.URL)
		}
	}
	if asset.Size <= 0 {
		return fmt.Errorf("%s size %d must be positive", field, asset.Size)
	}
	if len(asset.SHA256) != 64 {
		return fmt.Errorf("%s sha256 length %d, want 64", field, len(asset.SHA256))
	}
	if asset.SHA256 != strings.ToLower(asset.SHA256) {
		return fmt.Errorf("%s sha256 must be lowercase", field)
	}
	if _, err := hex.DecodeString(asset.SHA256); err != nil {
		return fmt.Errorf("%s has invalid sha256: %w", field, err)
	}
	return nil
}

func VerifyManifestJSON(encoded []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return Manifest{}, fmt.Errorf("ed25519 public key length %d, want %d", len(publicKey), ed25519.PublicKeySize)
	}
	if strings.ToLower(manifest.Signature.Algorithm) != "ed25519" {
		return Manifest{}, fmt.Errorf("unsupported signature algorithm %q", manifest.Signature.Algorithm)
	}
	sig, err := decodeSignatureValue(manifest.Signature.Value)
	if err != nil {
		return Manifest{}, err
	}
	canonical, err := CanonicalManifestJSONBytes(encoded)
	if err != nil {
		return Manifest{}, err
	}
	if !ed25519.Verify(publicKey, canonical, sig) {
		return Manifest{}, errors.New("manifest signature verification failed")
	}
	return manifest, nil
}

func VerifyManifestJSONWithPublicKeyPEM(encoded, publicKeyPEM []byte) (Manifest, error) {
	key, err := ParseEd25519PublicKeyPEM(publicKeyPEM)
	if err != nil {
		return Manifest{}, err
	}
	return VerifyManifestJSON(encoded, key)
}

func ParseEd25519PublicKeyPEM(publicKeyPEM []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return nil, errors.New("public key is not PEM encoded")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want ed25519.PublicKey", parsed)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key length %d, want %d", len(key), ed25519.PublicKeySize)
	}
	return key, nil
}

func ResolveAsset(part ManifestPart, targetSlot Slot) (ManifestAsset, error) {
	if targetSlot != SlotA && targetSlot != SlotB {
		return ManifestAsset{}, fmt.Errorf("invalid target slot %d", targetSlot)
	}
	if targetSlot == SlotA && part.AssetA != nil {
		if err := validateManifestAsset(part.Name+".asset_a", *part.AssetA, part.Name+"_a.img"); err != nil {
			return ManifestAsset{}, err
		}
		return *part.AssetA, nil
	}
	if targetSlot == SlotB && part.AssetB != nil {
		if err := validateManifestAsset(part.Name+".asset_b", *part.AssetB, part.Name+"_b.img"); err != nil {
			return ManifestAsset{}, err
		}
		return *part.AssetB, nil
	}
	if part.Name != "boot" && part.Asset != nil {
		if err := validateManifestAsset(part.Name+".asset", *part.Asset, part.Name+".img"); err != nil {
			return ManifestAsset{}, err
		}
		return *part.Asset, nil
	}
	return ManifestAsset{}, fmt.Errorf("part %q has no asset for target slot %d", part.Name, targetSlot)
}

func decodeSignatureValue(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("signature value is empty")
	}
	if sig, err := hex.DecodeString(value); err == nil {
		if len(sig) == ed25519.SignatureSize {
			return sig, nil
		}
	}
	sig, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("signature value is neither hex nor base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature length %d, want %d", len(sig), ed25519.SignatureSize)
	}
	return sig, nil
}
