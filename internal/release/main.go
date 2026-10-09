// Command release builds standalone release archives and SHA-256 checksums.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const commandName = "git-cow-worktree"

var targets = []string{
	"windows-amd64", "windows-arm64", "windows-386",
	"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64",
}

var safeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

func main() {
	version := flag.String("version", "", "release version (required)")
	target := flag.String("target", "", "OS-architecture target (default: all supported targets)")
	output := flag.String("output", "dist", "output directory")
	flag.Parse()
	if err := run(*version, *target, *output); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(version, target, output string) (err error) {
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if !safeVersion.MatchString(version) {
		return errors.New("version must start with an ASCII letter or digit and contain only letters, digits, '.', '_', '+', or '-'")
	}
	selected := targets
	if target != "" {
		found := false
		for _, supported := range targets {
			if target == supported {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported target %q (supported: %s)", target, strings.Join(targets, ", "))
		}
		selected = []string{target}
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(output, ".release-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	for _, target := range selected {
		if err := build(version, target, output, work); err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
	}
	return nil
}

func build(version, target, output, work string) error {
	goos, goarch, _ := strings.Cut(target, "-")
	binary := commandName
	ext := ".tar.gz"
	if goos == "windows" {
		binary += ".exe"
		ext = ".zip"
	}
	binaryPath := filepath.Join(work, binary)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", binaryPath, ".")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "CGO_ENABLED", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GO386", "GOFLAGS":
		default:
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	files := []archiveFile{
		{binaryPath, binary, 0755},
		{"README.md", "README.md", 0644},
		{"LICENSE", "LICENSE", 0644},
	}
	asset := fmt.Sprintf("%s_%s_%s_%s%s", commandName, version, goos, goarch, ext)
	assetPath := filepath.Join(work, asset)
	checksum, err := writeArchive(assetPath, files, goos == "windows")
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	checksumPath := assetPath + ".sha256"
	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%x  %s\n", checksum, asset)), 0644); err != nil {
		return fmt.Errorf("checksum: %w", err)
	}
	if err := os.Rename(assetPath, filepath.Join(output, asset)); err != nil {
		return err
	}
	if err := os.Rename(checksumPath, filepath.Join(output, asset+".sha256")); err != nil {
		return err
	}
	fmt.Println(filepath.Join(output, asset))
	return nil
}

type archiveFile struct {
	path string
	name string
	mode int64
}

func writeArchive(path string, files []archiveFile, windows bool) ([]byte, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	w := io.MultiWriter(f, hash)
	if windows {
		err = writeZip(w, files)
	} else {
		err = writeTarGzip(w, files)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func writeZip(w io.Writer, files []archiveFile) (err error) {
	zw := zip.NewWriter(w)
	defer func() { err = errors.Join(err, zw.Close()) }()
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(os.FileMode(file.mode))
		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if err := copyFile(entry, file.path); err != nil {
			return err
		}
	}
	return nil
}

func writeTarGzip(w io.Writer, files []archiveFile) (err error) {
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)
	defer func() { err = errors.Join(err, tw.Close(), gw.Close()) }()
	for _, file := range files {
		info, err := os.Stat(file.path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if err := copyFile(tw, file.path); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return errors.Join(err, f.Close())
}
