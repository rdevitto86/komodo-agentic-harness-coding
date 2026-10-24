package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// entryVersion is the schema version written into every entry file.
const entryVersion = 1

// entryFile is the JSON stored for one entry.
type entryFile struct {
	V       int    `json:"v"`       // schema version, 1
	SHA256  string `json:"sha256"`  // 64 hexadecimal characters: SHA-256 of the payload bytes
	Payload []byte `json:"payload"` // the payload; encoding/json writes it as base64
}

// namespaces lists every valid namespace.
var namespaces = []Namespace{NSSkills, NSContext, NSMap, NSSetup, NSPrompt}

// encodeEntry wraps the payload in an entry file holding its schema version and SHA-256.
func encodeEntry(payload []byte) []byte {
	if payload == nil {
		payload = []byte{}
	}

	sum := sha256.Sum256(payload)
	raw, err := json.Marshal(entryFile{V: entryVersion, SHA256: hex.EncodeToString(sum[:]), Payload: payload})
	if err != nil {
		panic("marshal cache entry: " + err.Error()) // a struct of an int, a string and bytes always marshals
	}
	return raw
}

// decodeEntry returns the payload of an entry file, or false when the JSON, the version or the hash is wrong.
func decodeEntry(raw []byte) ([]byte, bool) {
	var f entryFile
	if err := json.Unmarshal(raw, &f); err != nil || f.V != entryVersion {
		return nil, false
	}

	sum := sha256.Sum256(f.Payload)
	if f.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, false
	}

	return f.Payload, true
}

// validNamespace reports whether ns is one of the known namespaces.
func validNamespace(ns Namespace) bool {
	for _, n := range namespaces {
		if n == ns {
			return true
		}
	}
	return false
}

// validKey reports whether key is 64 lowercase hex characters.
func validKey(key Key) bool {
	if len(key) != 2*sha256.Size {
		return false
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
