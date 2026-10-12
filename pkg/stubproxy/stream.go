package stubproxy

import (
	"encoding/base64"
	"fmt"

	"infernosim/pkg/causal"
	"infernosim/pkg/event"
)

// A mutation can change byte lengths. Preserve the captured number of flushes
// and delays, filling original chunk lengths and putting any growth in the last
// chunk. This promises protocol frame integrity, not original TCP segmentation.
func reframeResponse(original []event.StreamFrame, body []byte) ([]event.StreamFrame, error) {
	if len(body) > 16<<20 || len(original) > 10000 {
		return nil, fmt.Errorf("mutated stream exceeds replay bounds")
	}
	result := make([]event.StreamFrame, len(original))
	offset := 0
	for i, f := range original {
		b, err := base64.StdEncoding.DecodeString(f.BodyB64)
		if err != nil {
			return nil, err
		}
		if f.BodySha256 != "" && causal.Digest(b) != f.BodySha256 {
			return nil, fmt.Errorf("captured response chunk digest mismatch")
		}
		end := offset + len(b)
		if i == len(original)-1 || end > len(body) {
			end = len(body)
		}
		chunk := body[offset:end]
		offset = end
		result[i] = event.StreamFrame{Delay: f.Delay, BodyB64: base64.StdEncoding.EncodeToString(chunk), BodySha256: causal.Digest(chunk)}
	}
	return result, nil
}
