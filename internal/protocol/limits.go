package protocol

const (
	// MaxUploadFileBytes keeps the doubly base64-encoded task envelope below the
	// agent's bounded HTTPS response reader, including JSON and AEAD overhead.
	MaxUploadFileBytes        = 40 * 1024 * 1024
	MaxRemotePathBytes        = 4096
	MaxHTTPSTaskResponseBytes = 72 * 1024 * 1024
	MaxUploadTaskPayloadBytes = MaxRemotePathBytes + 1 + ((MaxUploadFileBytes+2)/3)*4
)
