package causal

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const IdentityHeader = "X-InfernoSIM-Operation-ID"
const IdentityMeta = "infernosim/operation"

func Decode(body []byte) (any, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	err := d.Decode(&v)
	if err == nil && !json.Valid(body) {
		err = fmt.Errorf("invalid JSON document")
	}
	return v, err
}

func validRPCID(id string) bool {
	v, err := Decode([]byte(id))
	if err != nil {
		return false
	}
	switch value := v.(type) {
	case string:
		return value != ""
	case json.Number:
		_, err := value.Int64()
		return err == nil
	}
	return false
}

// RequestFingerprint removes only the JSON-RPC transport ID and our explicit
// identity metadata. Arguments and other protocol metadata remain significant.
func RequestFingerprint(protocol, method, host, path string, body []byte) (string, string, string) {
	identity, id := "", ""
	canonical := body
	if v, err := Decode(body); err == nil {
		if obj, ok := v.(map[string]any); ok && obj["jsonrpc"] == "2.0" {
			if value, exists := obj["id"]; exists {
				b, _ := json.Marshal(value)
				id = string(b)
				delete(obj, "id")
			}
			if params, ok := obj["params"].(map[string]any); ok {
				if meta, ok := params["_meta"].(map[string]any); ok {
					identity, _ = meta[IdentityMeta].(string)
					delete(meta, IdentityMeta)
					if len(meta) == 0 {
						delete(params, "_meta")
					}
				}
			}
		}
		canonical, _ = json.Marshal(v)
	}
	return Hash(struct{ Protocol, Method, Host, Path, Body string }{protocol, method, host, path, Digest(canonical)}), identity, id
}

func RemapResponse(body []byte, runtimeID string) ([]byte, error) {
	if runtimeID == "" {
		return body, nil
	}
	v, err := Decode(body)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok || obj["jsonrpc"] != "2.0" {
		return body, nil
	}
	id, err := Decode([]byte(runtimeID))
	if err != nil {
		return nil, err
	}
	obj["id"] = id
	return json.Marshal(obj)
}
