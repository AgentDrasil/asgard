package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// rewriteContentToParts is a compatibility middleware for @a2a-js/sdk@0.3.x clients.
//
// The 0.3.x REST transport sends message bodies with the field name "content"
// for message parts (old A2A spec), while the Go server (a2a-go v2.3+) expects
// "parts". This middleware rewrites the field name in POST request bodies.
//
// Before: {"message": {"content": [{"text": "hello"}], ...}}
// After:  {"message": {"parts":   [{"text": "hello"}], ...}}
func rewriteContentToParts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}

		body = rewriteMessageContentField(body)

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

// rewriteMessageContentField renames the "content" key inside "message" to "parts".
// It operates on the raw JSON to avoid losing any fields not known to our schema.
func rewriteMessageContentField(data []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data // not JSON we recognise — pass through unchanged
	}

	msgRaw, ok := raw["message"]
	if !ok {
		return data
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return data
	}

	contentRaw, hasContent := msg["content"]
	_, hasParts := msg["parts"]

	if !hasContent || hasParts {
		return data // nothing to rewrite
	}

	msg["parts"] = contentRaw
	delete(msg, "content")

	newMsg, err := json.Marshal(msg)
	if err != nil {
		return data
	}
	raw["message"] = newMsg

	out, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return out
}
