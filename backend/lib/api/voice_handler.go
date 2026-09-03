package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// DefaultGoogleAuthURL is the Google GenAI auth tokens endpoint.
	DefaultGoogleAuthURL = "https://generativelanguage.googleapis.com/v1beta/auth_tokens"
	// TranscribeLiveModel is the Gemini live transcription model identifier.
	TranscribeLiveModel = "models/gemini-3.5-transcribe-live"
	// VoiceTokenTTL is the default lifetime for ephemeral live tokens.
	VoiceTokenTTL = 15 * time.Minute
)

// googleAuthTokenReq is the payload sent to Google auth_tokens API.
type googleAuthTokenReq struct {
	Uses                     int                       `json:"uses,omitempty"`
	ExpireTime               string                    `json:"expireTime,omitempty"`
	BidiGenerateContentSetup *bidiGenerateContentSetup `json:"bidiGenerateContentSetup,omitempty"`
	FieldMask                string                    `json:"fieldMask,omitempty"`
}

type bidiGenerateContentSetup struct {
	Model string `json:"model,omitempty"`
}

// googleAuthTokenResp is the defensive response model parsed from Google auth_tokens API.
type googleAuthTokenResp struct {
	Name       string `json:"name"`
	Token      string `json:"token"`
	ExpireTime string `json:"expireTime"`
}

// VoiceTokenResponse is returned to the client.
type VoiceTokenResponse struct {
	Token      string `json:"token"`
	ExpireTime string `json:"expireTime"`
	Model      string `json:"model"`
}

func writeVoiceError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleCreateVoiceToken issues an ephemeral token restricted to Gemini live voice transcription.
func (s *Server) handleCreateVoiceToken(w http.ResponseWriter, r *http.Request) {
	if s.geminiAPIKey == "" {
		writeVoiceError(w, http.StatusServiceUnavailable, "Gemini API key is not configured")
		return
	}

	authURL := s.voiceAuthURL
	if authURL == "" {
		authURL = DefaultGoogleAuthURL
	}

	httpClient := s.voiceHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	expireTime := time.Now().Add(VoiceTokenTTL).Format(time.RFC3339)
	reqBody := googleAuthTokenReq{
		Uses:       1,
		ExpireTime: expireTime,
		BidiGenerateContentSetup: &bidiGenerateContentSetup{
			Model: TranscribeLiveModel,
		},
		FieldMask: "model",
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal voice token request")
		writeVoiceError(w, http.StatusInternalServerError, "failed to create voice token request")
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, authURL, bytes.NewReader(reqBytes))
	if err != nil {
		log.Error().Err(err).Msg("failed to create upstream voice token request")
		writeVoiceError(w, http.StatusInternalServerError, "failed to initialize upstream request")
		return
	}

	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("x-goog-api-key", s.geminiAPIKey)

	resp, err := httpClient.Do(upstreamReq)
	if err != nil {
		log.Error().Err(err).Msg("failed to reach Google voice auth service")
		writeVoiceError(w, http.StatusBadGateway, fmt.Sprintf("failed to contact upstream voice auth service: %v", err))
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Error().Err(err).Msg("failed to read response from Google voice auth service")
		writeVoiceError(w, http.StatusBadGateway, "failed to read upstream voice auth response")
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Str("response", string(bodyBytes)).Msg("upstream voice auth returned error")
		writeVoiceError(w, http.StatusBadGateway, fmt.Sprintf("upstream voice auth error (status %d): %s", resp.StatusCode, string(bodyBytes)))
		return
	}

	var upstreamResp googleAuthTokenResp
	if err := json.Unmarshal(bodyBytes, &upstreamResp); err != nil {
		log.Error().Err(err).Str("response", string(bodyBytes)).Msg("failed to decode upstream voice token response")
		writeVoiceError(w, http.StatusBadGateway, "invalid response from upstream voice auth")
		return
	}

	token := upstreamResp.Token
	if token == "" {
		token = upstreamResp.Name
	}
	if token == "" {
		log.Error().Str("response", string(bodyBytes)).Msg("upstream voice auth response missing token/name")
		writeVoiceError(w, http.StatusBadGateway, "upstream voice auth response missing token")
		return
	}

	outExp := upstreamResp.ExpireTime
	if outExp == "" {
		outExp = expireTime
	}

	clientResp := VoiceTokenResponse{
		Token:      token,
		ExpireTime: outExp,
		Model:      TranscribeLiveModel,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(clientResp)
}
