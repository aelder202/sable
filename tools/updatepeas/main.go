package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const (
	maxPEASToolBytes = 5 * 1024 * 1024
	linPEASURL       = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/linpeas.sh"
	winPEASURL       = "https://github.com/peass-ng/PEASS-ng/releases/latest/download/winPEAS.bat"
)

type peasAsset struct {
	name string
	url  string
	path string
}

func main() {
	assets := []peasAsset{
		{name: "LinPEAS", url: linPEASURL, path: filepath.FromSlash("internal/agent/peas/linpeas.sh")},
		{name: "winPEAS", url: winPEASURL, path: filepath.FromSlash("internal/agent/peas/winPEAS.bat")},
	}

	for _, asset := range assets {
		if err := updateAsset(asset); err != nil {
			fmt.Fprintf(os.Stderr, "update %s: %v\n", asset.name, err)
			os.Exit(1)
		}
	}
}

func updateAsset(asset peasAsset) error {
	fmt.Printf("[peas] downloading %s from %s\n", asset.name, asset.url)
	req, err := http.NewRequest(http.MethodGet, asset.url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
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

	if err := os.MkdirAll(filepath.Dir(asset.path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(asset.path, data, 0600); err != nil {
		return err
	}
	fmt.Printf("[peas] wrote %s (%d bytes)\n", asset.path, len(data))
	return nil
}
