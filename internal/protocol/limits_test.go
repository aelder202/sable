package protocol_test

import (
	"encoding/base64"
	"testing"

	"github.com/aelder202/sable/internal/protocol"
)

func TestMaximumUploadFitsBoundedHTTPSResponse(t *testing.T) {
	// Payload base64 is JSON-safe. Reserve another full remote-path length for
	// worst-case JSON escaping, generous task metadata overhead, and AES-GCM's
	// nonce/tag before the envelope applies its second base64 expansion.
	const taskJSONOverhead = 512
	plaintextUpperBound := protocol.MaxUploadTaskPayloadBytes + protocol.MaxRemotePathBytes + taskJSONOverhead
	ciphertextUpperBound := plaintextUpperBound + 12 + 16
	envelopeUpperBound := base64.StdEncoding.EncodedLen(ciphertextUpperBound) + 128
	if envelopeUpperBound >= protocol.MaxHTTPSTaskResponseBytes {
		t.Fatalf("maximum upload envelope upper bound %d exceeds HTTPS response cap %d", envelopeUpperBound, protocol.MaxHTTPSTaskResponseBytes)
	}
}
