package api

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

const (
	maxSleepSeconds             = 24 * 60 * 60
	maxRemotePath               = 4096
	maxStandardTaskPayloadBytes = 48 * 1024
	maxUploadFileBytes          = 50 * 1024 * 1024
	maxUploadTaskPayloadBytes   = maxRemotePath + 1 + ((maxUploadFileBytes+2)/3)*4
)

func maxTaskPayloadBytes(taskType string) int {
	if taskType == "upload" {
		return maxUploadTaskPayloadBytes
	}
	return maxStandardTaskPayloadBytes
}

func validateTaskRequest(taskType, payload string) error {
	switch taskType {
	case "shell":
		if strings.TrimSpace(payload) == "" {
			return errors.New("shell payload required")
		}
		if hasDisallowedBinary(payload) {
			return errors.New("shell payload contains invalid control characters")
		}
	case "download":
		if strings.TrimSpace(payload) == "" {
			return errors.New("download path required")
		}
		if hasDisallowedPathChars(payload) {
			return errors.New("download path contains invalid characters")
		}
	case "complete":
		if strings.TrimSpace(payload) == "" {
			return errors.New("completion path required")
		}
		if hasDisallowedPathChars(payload) {
			return errors.New("completion path contains invalid characters")
		}
	case "ls":
		if strings.TrimSpace(payload) == "" {
			return errors.New("ls path required")
		}
		if hasDisallowedPathChars(payload) {
			return errors.New("ls path contains invalid characters")
		}
	case "pathbrowse":
		value := strings.TrimSpace(payload)
		if value != "start" && value != "stop" {
			return errors.New("pathbrowse payload must be start or stop")
		}
	case "upload":
		if err := validateUploadPayload(payload); err != nil {
			return err
		}
	case "sleep":
		trimmed := strings.TrimSpace(payload)
		secs, err := strconv.Atoi(trimmed)
		if err != nil || secs < 1 || secs > maxSleepSeconds {
			return errors.New("sleep must be an integer between 1 and 86400")
		}
	case "kill":
		if strings.TrimSpace(payload) != "" {
			return errors.New("kill does not accept a payload")
		}
	case "ps", "screenshot", "persistence", "peas", "snapshot":
		if strings.TrimSpace(payload) != "" {
			return errors.New(taskType + " does not accept a payload")
		}
	case "cancel":
		if strings.TrimSpace(payload) == "" || len(strings.TrimSpace(payload)) > 64 {
			return errors.New("cancel requires a task id payload")
		}
		if strings.ContainsAny(payload, "\x00\r\n") {
			return errors.New("cancel payload contains invalid characters")
		}
	case "interactive":
		switch strings.TrimSpace(payload) {
		case "start", "stop":
		default:
			return errors.New("interactive payload must be start or stop")
		}
	default:
		return errors.New("invalid task type")
	}

	return nil
}

func normalizeTaskPayload(taskType, payload string) string {
	switch taskType {
	case "sleep", "interactive", "complete", "pathbrowse", "ls", "cancel":
		return strings.TrimSpace(payload)
	case "kill", "ps", "screenshot", "persistence", "peas", "snapshot":
		return ""
	default:
		return payload
	}
}

func validateUploadPayload(payload string) error {
	idx := strings.LastIndexByte(payload, ':')
	if idx <= 0 || idx >= len(payload)-1 {
		return errors.New("upload payload must be path:base64data")
	}

	path := payload[:idx]
	if len(path) > maxRemotePath {
		return errors.New("upload path too long")
	}
	if hasDisallowedPathChars(path) {
		return errors.New("upload path contains invalid characters")
	}

	encoded := payload[idx+1:]
	if len(encoded) > base64.StdEncoding.EncodedLen(maxUploadFileBytes) {
		return errors.New("upload data too large")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("upload data must be valid base64")
	}
	if len(data) > maxUploadFileBytes {
		return errors.New("upload data too large")
	}

	return nil
}

func hasDisallowedPathChars(value string) bool {
	return value == "" || len(value) > maxRemotePath || strings.ContainsAny(value, "\x00\r\n")
}

func hasDisallowedBinary(value string) bool {
	return strings.ContainsRune(value, '\x00')
}
