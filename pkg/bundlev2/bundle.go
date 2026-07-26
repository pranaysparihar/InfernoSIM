package bundlev2

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	Format          = "infernosim.bundle"
	Version         = 2
	kdfIterations   = 600_000
	maxBundleSize   = 1 << 30
	maxArchivedFile = 256 << 20
)

var associatedData = []byte("infernosim.bundle:v2")

type Envelope struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	KDF        KDF    `json:"kdf"`
	Cipher     Cipher `json:"cipher"`
	Ciphertext []byte `json:"ciphertext"`
}

type KDF struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
	Salt       []byte `json:"salt"`
}

type Cipher struct {
	Name  string `json:"name"`
	Nonce []byte `json:"nonce"`
}

func SealDirectory(sourceDir, outputPath string, passphrase []byte) error {
	if len(passphrase) < 12 {
		return fmt.Errorf("bundle passphrase must contain at least 12 bytes")
	}
	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("open incident directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bundle source must be a directory")
	}
	plaintext, err := archiveDirectory(sourceDir)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate bundle salt: %w", err)
	}
	key := pbkdf2SHA256(passphrase, salt, kdfIterations, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate bundle nonce: %w", err)
	}
	envelope := Envelope{
		Format:  Format,
		Version: Version,
		KDF: KDF{
			Name:       "PBKDF2-HMAC-SHA256",
			Iterations: kdfIterations,
			Salt:       salt,
		},
		Cipher: Cipher{
			Name:  "AES-256-GCM",
			Nonce: nonce,
		},
		Ciphertext: gcm.Seal(nil, nonce, plaintext, associatedData),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(outputPath), ".infernosim-bundle-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing bundle %q", outputPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempName, outputPath)
}

func OpenToDirectory(bundlePath, destination string, passphrase []byte) error {
	if len(passphrase) == 0 {
		return fmt.Errorf("bundle passphrase is required")
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return err
	}
	if info.Size() > maxBundleSize {
		return fmt.Errorf("encrypted bundle exceeds 1 GiB safety limit")
	}
	encoded, err := os.ReadFile(bundlePath)
	if err != nil {
		return err
	}
	var envelope Envelope
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&envelope); err != nil {
		return fmt.Errorf("parse encrypted bundle: %w", err)
	}
	if envelope.Format != Format || envelope.Version != Version {
		return fmt.Errorf("unsupported bundle format/version %q/%d", envelope.Format, envelope.Version)
	}
	if envelope.KDF.Name != "PBKDF2-HMAC-SHA256" ||
		envelope.KDF.Iterations < 100_000 ||
		envelope.KDF.Iterations > 2_000_000 ||
		len(envelope.KDF.Salt) != 16 {
		return fmt.Errorf("unsupported or unsafe bundle KDF parameters")
	}
	if envelope.Cipher.Name != "AES-256-GCM" {
		return fmt.Errorf("unsupported bundle cipher %q", envelope.Cipher.Name)
	}
	key := pbkdf2SHA256(passphrase, envelope.KDF.Salt, envelope.KDF.Iterations, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(envelope.Cipher.Nonce) != gcm.NonceSize() {
		return fmt.Errorf("invalid bundle nonce")
	}
	plaintext, err := gcm.Open(nil, envelope.Cipher.Nonce, envelope.Ciphertext, associatedData)
	if err != nil {
		return fmt.Errorf("decrypt bundle: authentication failed")
	}
	return extractArchive(plaintext, destination)
}

func archiveDirectory(sourceDir string) ([]byte, error) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	err := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return fmt.Errorf("unsafe bundle path %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle does not permit symbolic links: %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle only permits regular files: %s", relative)
		}
		if info.Size() > maxArchivedFile {
			return fmt.Errorf("bundle file %s exceeds 256 MiB safety limit", relative)
		}
		header := &tar.Header{
			Name:    filepath.ToSlash(relative),
			Mode:    0o600,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		return nil, err
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > maxBundleSize {
		return nil, fmt.Errorf("bundle archive exceeds 1 GiB safety limit")
	}
	return compressed.Bytes(), nil
}

func extractArchive(archive []byte, destination string) error {
	if info, err := os.Stat(destination); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("bundle destination is not a directory")
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return fmt.Errorf("refusing to extract into non-empty directory %q", destination)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open bundle archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(io.LimitReader(gzipReader, maxBundleSize+1))
	var total int64
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("bundle contains unsupported entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxArchivedFile {
			return fmt.Errorf("bundle entry %q has unsafe size", header.Name)
		}
		total += header.Size
		if total > maxBundleSize {
			return fmt.Errorf("extracted bundle exceeds 1 GiB safety limit")
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return fmt.Errorf("bundle contains unsafe path %q", header.Name)
		}
		target := filepath.Join(destination, clean)
		if !strings.HasPrefix(target, filepath.Clean(destination)+string(filepath.Separator)) {
			return fmt.Errorf("bundle path escapes destination: %q", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("refusing to overwrite extracted file %q", target)
		} else if !os.IsNotExist(err) {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, tarReader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	hashLength := sha256.Size
	blocks := (keyLength + hashLength - 1) / hashLength
	result := make([]byte, 0, blocks*hashLength)
	for blockIndex := 1; blockIndex <= blocks; blockIndex++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write([]byte{
			byte(blockIndex >> 24),
			byte(blockIndex >> 16),
			byte(blockIndex >> 8),
			byte(blockIndex),
		})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for iteration := 1; iteration < iterations; iteration++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLength]
}
