package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aelder202/sable/internal/securefile"
)

const (
	maxPEASToolBytes = 5 * 1024 * 1024
	peasReleaseTag   = "20260715-81d3c7f8"
	linPEASURL       = "https://github.com/peass-ng/PEASS-ng/releases/download/" + peasReleaseTag + "/linpeas.sh"
	winPEASURL       = "https://github.com/peass-ng/PEASS-ng/releases/download/" + peasReleaseTag + "/winPEAS.bat"
	linPEASSHA256    = "9316493abe0f2a2dad7f94d623bd917ac990c9ddd7537ddac9d715a5410e28a8"
	winPEASSHA256    = "7d76f460601f19577f5744154af45ec0ad993cf8f9d1069d2e910aead15db5f6"
)

type peasAsset struct {
	name   string
	url    string
	path   string
	sha256 string
}

func main() {
	assets := []peasAsset{
		{name: "LinPEAS", url: linPEASURL, path: filepath.FromSlash("internal/agent/peas/linpeas.sh"), sha256: linPEASSHA256},
		{name: "winPEAS", url: winPEASURL, path: filepath.FromSlash("internal/agent/peas/winPEAS.bat"), sha256: winPEASSHA256},
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-HTTPS redirect")
			}
			return nil
		},
	}

	for _, asset := range assets {
		if err := updateAsset(client, asset); err != nil {
			fmt.Fprintf(os.Stderr, "update %s: %v\n", asset.name, err)
			os.Exit(1)
		}
	}
}

func updateAsset(client *http.Client, asset peasAsset) error {
	fmt.Printf("[peas] downloading pinned %s release %s\n", asset.name, peasReleaseTag)
	req, err := http.NewRequest(http.MethodGet, asset.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "sable-pinned-peas-updater/1")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPEASToolBytes+1))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty response")
	}
	if len(data) > maxPEASToolBytes {
		return fmt.Errorf("response exceeds %d bytes", maxPEASToolBytes)
	}
	expectedDigest, err := hex.DecodeString(asset.sha256)
	if err != nil || len(expectedDigest) != sha256.Size {
		return fmt.Errorf("invalid pinned SHA-256 for %s", asset.name)
	}
	actualDigest := sha256.Sum256(data)
	if !equalBytes(actualDigest[:], expectedDigest) {
		return fmt.Errorf("SHA-256 mismatch for %s: got %x, want %s", asset.name, actualDigest, asset.sha256)
	}

	if err := os.MkdirAll(filepath.Dir(asset.path), 0700); err != nil {
		return err
	}
	if err := securefile.WriteFile(asset.path, data); err != nil {
		return err
	}
	fmt.Printf("[peas] verified sha256:%x and wrote %s (%d bytes)\n", actualDigest, asset.path, len(data))
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var difference byte
	for i := range a {
		difference |= a[i] ^ b[i]
	}
	return difference == 0
}
