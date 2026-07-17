package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

const (
	maxArchiveBytes       = maxDownloadBytes
	maxArchiveSourceBytes = 250 * 1024 * 1024
	maxArchiveEntries     = 10_000
	maxArchiveWarnings    = 25
	archiveProgressEvery  = time.Second
)

var errArchiveTooLarge = errors.New("archive exceeded the 50 MiB transfer limit")

type archiveArtifactResult struct {
	MIME        string   `json:"mime"`
	Filename    string   `json:"filename"`
	Data        string   `json:"data"`
	FileCount   int      `json:"file_count"`
	SourceBytes int64    `json:"source_bytes"`
	Skipped     []string `json:"skipped,omitempty"`
}

type archiveSelectionRequest struct {
	Paths []string `json:"paths"`
	Base  string   `json:"base,omitempty"`
}

type archiveSelection struct {
	Roots       []string
	Base        string
	DisplayPath string
	Filename    string
	RootAtBase  bool
	RootLabel   string
}

type transferProgress struct {
	Kind         string `json:"kind"`
	Phase        string `json:"phase"`
	Path         string `json:"path,omitempty"`
	Files        int    `json:"files,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	TotalBytes   int64  `json:"total_bytes,omitempty"`
	ArchiveBytes int    `json:"archive_bytes,omitempty"`
	Message      string `json:"message"`
}

type limitedArchiveBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedArchiveBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		return 0, errArchiveTooLarge
	}
	return b.buf.Write(p)
}

func startArchiveTask(taskID, path string) *protocol.TaskResult {
	displayPath := strings.TrimSpace(path)
	if selection, err := resolveArchiveSelection(path); err == nil {
		displayPath = selection.DisplayPath
	}
	atomic.AddInt32(&backgroundTaskCount, 1)
	ctx, cancel := context.WithCancel(context.Background())
	backgroundTasks.Store(taskID, cancel)
	go func() {
		defer backgroundTasks.Delete(taskID)
		defer atomic.AddInt32(&backgroundTaskCount, -1)
		output, taskErr := archiveDirectoryWithProgress(ctx, path, func(progress transferProgress) {
			queueAsyncTypedProgress(taskID, "archive_progress", "archive", encodeTransferProgress(progress))
		})
		queueAsyncResult(&protocol.TaskResult{TaskID: taskID, Type: "download_archive", Output: output, Error: taskErr})
	}()

	return &protocol.TaskResult{
		TaskID: taskID + "-archive-started",
		Type:   "archive_progress",
		Output: encodeTransferProgress(transferProgress{
			Kind:    "archive",
			Phase:   "preparing",
			Path:    displayPath,
			Message: "Preparing directory archive",
		}),
	}
}

func archiveDirectory(path string) (string, string) {
	return archiveDirectoryWithProgress(context.Background(), path, nil)
}

func archiveDirectoryWithProgress(ctx context.Context, path string, progress func(transferProgress)) (string, string) {
	selection, err := resolveArchiveSelection(path)
	if err != nil {
		return "", err.Error()
	}

	out := &limitedArchiveBuffer{limit: maxArchiveBytes}
	writer := zip.NewWriter(out)
	files := 0
	entries := 0
	var sourceBytes int64
	warnings := make([]string, 0)
	lastProgress := time.Now()
	emit := func(force bool, phase, message string) {
		if progress == nil || (!force && time.Since(lastProgress) < archiveProgressEvery) {
			return
		}
		progress(transferProgress{
			Kind:         "archive",
			Phase:        phase,
			Path:         selection.DisplayPath,
			Files:        files,
			Bytes:        sourceBytes,
			TotalBytes:   maxArchiveSourceBytes,
			ArchiveBytes: out.buf.Len(),
			Message:      message,
		})
		lastProgress = time.Now()
	}
	addWarning := func(message string) {
		if len(warnings) < maxArchiveWarnings {
			warnings = append(warnings, message)
		}
	}

	emit(true, "preparing", "Scanning and compressing selection")
	seen := make(map[string]bool)
	walkEntry := func(current string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			addWarning(current + ": " + walkErr.Error())
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		cleanCurrent := filepath.Clean(current)
		if seen[cleanCurrent] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		seen[cleanCurrent] = true
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("directory contains more than %d entries", maxArchiveEntries)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			addWarning(current + ": " + infoErr.Error())
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			addWarning(current + ": symbolic link skipped")
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			addWarning(current + ": non-regular file skipped")
			return nil
		}

		rel, relErr := filepath.Rel(selection.Base, current)
		if relErr == nil && selection.RootAtBase {
			if rel == "." {
				rel = selection.RootLabel
			} else {
				rel = filepath.Join(selection.RootLabel, rel)
			}
		}
		if relErr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if relErr != nil {
				return relErr
			}
			return errors.New("invalid archive entry path")
		}
		name := filepath.ToSlash(rel)
		header, headerErr := zip.FileInfoHeader(info)
		if headerErr != nil {
			return headerErr
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
			_, headerErr = writer.CreateHeader(header)
			return headerErr
		}

		if sourceBytes+info.Size() > maxArchiveSourceBytes {
			return fmt.Errorf("directory source data exceeds %s", formatByteCount(maxArchiveSourceBytes))
		}
		handle, openErr := os.Open(current)
		if openErr != nil {
			addWarning(current + ": " + openErr.Error())
			return nil
		}
		header.Method = zip.Deflate
		destination, createErr := writer.CreateHeader(header)
		if createErr != nil {
			_ = handle.Close()
			return createErr
		}
		copyErr := copyArchiveFile(ctx, destination, handle, &sourceBytes, func() {
			emit(false, "compressing", "Compressing directory contents")
		})
		closeErr := handle.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files++
		emit(false, "compressing", "Compressed "+strconvFileCount(files))
		return nil
	}
	var walkErr error
	for _, root := range selection.Roots {
		if _, err := os.Lstat(root); err != nil {
			walkErr = err
			break
		}
		if err := filepath.WalkDir(root, walkEntry); err != nil {
			walkErr = err
			break
		}
	}
	if walkErr != nil {
		_ = writer.Close()
		if errors.Is(walkErr, context.Canceled) {
			return "", "archive cancelled"
		}
		if errors.Is(walkErr, errArchiveTooLarge) {
			return "", errArchiveTooLarge.Error()
		}
		return "", walkErr.Error()
	}
	if err := writer.Close(); err != nil {
		if errors.Is(err, errArchiveTooLarge) {
			return "", errArchiveTooLarge.Error()
		}
		return "", err.Error()
	}
	if err := ctx.Err(); err != nil {
		return "", "archive cancelled"
	}

	result := archiveArtifactResult{
		MIME:        "application/zip",
		Filename:    selection.Filename,
		Data:        base64.StdEncoding.EncodeToString(out.buf.Bytes()),
		FileCount:   files,
		SourceBytes: sourceBytes,
		Skipped:     warnings,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err.Error()
	}
	emit(true, "ready", fmt.Sprintf("Archive ready: %d files, %s", files, formatByteCount(int64(out.buf.Len()))))
	return string(encoded), ""
}

func resolveArchiveSelection(payload string) (archiveSelection, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return archiveSelection{}, errors.New("archive path required")
	}
	request := archiveSelectionRequest{Paths: []string{payload}}
	if strings.HasPrefix(payload, "{") {
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			return archiveSelection{}, errors.New("invalid archive selection request")
		}
	}
	if len(request.Paths) == 0 || len(request.Paths) > 100 {
		return archiveSelection{}, errors.New("archive selection must contain between 1 and 100 paths")
	}
	roots := make([]string, 0, len(request.Paths))
	for _, selected := range request.Paths {
		selected = strings.TrimSpace(selected)
		if selected == "" {
			return archiveSelection{}, errors.New("archive selection contains an empty path")
		}
		root, err := filepath.Abs(selected)
		if err != nil {
			return archiveSelection{}, err
		}
		roots = append(roots, filepath.Clean(root))
	}
	base := strings.TrimSpace(request.Base)
	if base != "" {
		absoluteBase, err := filepath.Abs(base)
		if err != nil {
			return archiveSelection{}, err
		}
		base = filepath.Clean(absoluteBase)
	} else {
		base = filepath.Dir(roots[0])
	}
	rootAtBase := false
	rootLabel := ""
	for _, root := range roots {
		rel, err := filepath.Rel(base, root)
		if err == nil && rel == "." && len(roots) == 1 && filepath.Clean(base) == filepath.Clean(root) {
			rootAtBase = true
			rootLabel = archiveRootLabel(root)
			continue
		}
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return archiveSelection{}, errors.New("archive selections must be children of the selected base directory")
		}
	}
	filename := ""
	displayPath := base
	if len(roots) == 1 {
		filename = archiveDownloadName(roots[0])
		displayPath = roots[0]
	} else {
		filename = archiveSelectionDownloadName(base)
	}
	return archiveSelection{
		Roots:       roots,
		Base:        base,
		DisplayPath: displayPath,
		Filename:    filename,
		RootAtBase:  rootAtBase,
		RootLabel:   rootLabel,
	}, nil
}

func copyArchiveFile(ctx context.Context, destination io.Writer, source io.Reader, total *int64, tick func()) error {
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			*total += int64(n)
			if *total > maxArchiveSourceBytes {
				return fmt.Errorf("directory source data exceeds %s", formatByteCount(maxArchiveSourceBytes))
			}
			if _, err := destination.Write(buffer[:n]); err != nil {
				return err
			}
			if tick != nil {
				tick()
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func encodeTransferProgress(progress transferProgress) string {
	encoded, err := json.Marshal(progress)
	if err != nil {
		return progress.Message
	}
	return string(encoded)
}

func archiveDownloadName(root string) string {
	return sanitizeArchiveDownloadBase(filepath.Base(root), "remote-directory") + ".zip"
}

func archiveSelectionDownloadName(base string) string {
	return sanitizeArchiveDownloadBase(filepath.Base(base), "remote") + "-selection.zip"
}

func archiveRootLabel(root string) string {
	label := filepath.VolumeName(root)
	if label == "" {
		label = filepath.Base(root)
	}
	return sanitizeArchiveDownloadBase(label, "remote-root")
}

func sanitizeArchiveDownloadBase(name, fallback string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = fallback
	}
	if len(name) > 160 {
		name = name[:160]
	}
	return name
}

func strconvFileCount(count int) string {
	if count == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", count)
}
