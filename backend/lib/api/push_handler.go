package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2/google"

	"github.com/AgentDrasil/asgard/backend/lib/config"
)

var (
	pushTokensMu sync.Mutex
	pushTokens   = make(map[string]time.Time)

	fcmClientMu     sync.Mutex
	cachedFcmClient *http.Client
	cachedProjectID string
	cachedSaPath    string
	cachedFcmInit   bool
)

type RegisterPushTokenRequest struct {
	Token string `json:"token"`
}

type serviceAccountFile struct {
	ProjectID string `json:"project_id"`
}

func getFCMClient(cfg *config.FirebaseWebpushWebConfig) (*http.Client, string) {
	fcmClientMu.Lock()
	defer fcmClientMu.Unlock()

	var saPath string
	if cfg != nil {
		saPath = strings.TrimSpace(cfg.ServiceAccountFile)
	}

	if cachedFcmInit && cachedSaPath == saPath {
		return cachedFcmClient, cachedProjectID
	}

	cachedSaPath = saPath
	cachedFcmInit = true
	cachedFcmClient = nil
	cachedProjectID = ""

	if saPath == "" {
		log.Warn().Msg("FCM Service Account JSON not configured (set firebase_webpush_web.service_account_file in config). Falling back to direct HTTP post.")
		cachedFcmClient = &http.Client{Timeout: 10 * time.Second}
		return cachedFcmClient, cachedProjectID
	}

	foundPath, saData, projectID, saErr := findServiceAccountFile(cfg)
	if saErr == nil {
		ctx := context.Background()
		jwtCfg, err := google.JWTConfigFromJSON(saData, "https://www.googleapis.com/auth/firebase.messaging")
		if err == nil {
			cachedFcmClient = jwtCfg.Client(ctx)
			cachedProjectID = projectID
			log.Info().Str("service_account", foundPath).Str("project_id", projectID).Msg("FCM HTTP v1 authenticated client initialized")
		} else {
			log.Warn().Err(err).Msg("Failed to parse Service Account JSON for FCM OAuth2")
			cachedFcmClient = &http.Client{Timeout: 10 * time.Second}
		}
	} else {
		log.Warn().Err(saErr).Str("service_account_file", saPath).Msg("Failed to load FCM Service Account JSON. Falling back to direct HTTP post.")
		cachedFcmClient = &http.Client{Timeout: 10 * time.Second}
	}

	return cachedFcmClient, cachedProjectID
}

func resetFCMClientForTest() {
	fcmClientMu.Lock()
	defer fcmClientMu.Unlock()
	cachedFcmInit = false
	cachedFcmClient = nil
	cachedProjectID = ""
	cachedSaPath = ""
}

func (s *Server) handleRegisterPushToken(w http.ResponseWriter, r *http.Request) {
	var req RegisterPushTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}

	pushTokensMu.Lock()
	pushTokens[req.Token] = time.Now()
	pushTokensMu.Unlock()

	log.Info().Str("token_prefix", req.Token[:min(len(req.Token), 10)]).Msg("Registered FCM push token")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func findServiceAccountFile(cfg *config.FirebaseWebpushWebConfig) (path string, data []byte, projectID string, err error) {
	if cfg == nil || strings.TrimSpace(cfg.ServiceAccountFile) == "" {
		return "", nil, "", errors.New("service_account_file not configured")
	}
	path = strings.TrimSpace(cfg.ServiceAccountFile)
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		return path, nil, "", readErr
	}
	var sa serviceAccountFile
	if jsonErr := json.Unmarshal(b, &sa); jsonErr != nil {
		return path, nil, "", jsonErr
	}
	if sa.ProjectID == "" {
		return path, nil, "", errors.New("service account JSON missing project_id")
	}
	return path, b, sa.ProjectID, nil
}

func CheckPushNotificationSetup(cfg *config.FirebaseWebpushWebConfig) {
	if cfg == nil || strings.TrimSpace(cfg.ServiceAccountFile) == "" {
		log.Warn().Msg("FCM Service Account JSON not configured! Web Push notifications will not work until service_account_file is provided under firebase_webpush_web.")
		return
	}
	saPath, _, projectID, err := findServiceAccountFile(cfg)
	if err != nil {
		log.Warn().Err(err).Str("service_account_file", saPath).Msg("FCM Service Account JSON failed to load")
	} else {
		log.Info().Str("path", saPath).Str("project_id", projectID).Msg("FCM Service Account JSON loaded successfully")
	}
}

func (s *Server) SendPushNotification(chatID string, questionText string, agentName string) {
	go func() {
		pushTokensMu.Lock()
		tokens := make([]string, 0, len(pushTokens))
		for t := range pushTokens {
			tokens = append(tokens, t)
		}
		pushTokensMu.Unlock()

		log.Info().Str("chat_id", chatID).Int("token_count", len(tokens)).Msg("SendPushNotification called for ask-user event")

		if len(tokens) == 0 {
			log.Warn().Str("chat_id", chatID).Msg("SendPushNotification: No registered FCM tokens. Make sure browser opened WebUI and allowed push notifications.")
			return
		}

		var cfg *config.FirebaseWebpushWebConfig
		if s != nil && s.conf != nil {
			cfg = s.conf.FirebaseWebpushWeb
		}
		httpClient, projectID := getFCMClient(cfg)
		if httpClient == nil {
			return
		}

		title := "Ask User: Agent Needs Your Input"
		if agentName != "" {
			title = "Ask User (" + agentName + ")"
		}

		targetURL := "/chat/" + chatID

		for _, tok := range tokens {
			if projectID != "" {
				// FCM HTTP v1 API
				fcmV1Endpoint := "https://fcm.googleapis.com/v1/projects/" + projectID + "/messages:send"
				v1Payload := map[string]any{
					"message": map[string]any{
						"token": tok,
						"notification": map[string]string{
							"title": title,
							"body":  questionText,
						},
						"data": map[string]string{
							"chatID": chatID,
							"url":    targetURL,
							"title":  title,
							"body":   questionText,
						},
						"webpush": map[string]any{
							"fcm_options": map[string]string{
								"link": targetURL,
							},
						},
					},
				}
				bodyBytes, _ := json.Marshal(v1Payload)
				req, err := http.NewRequest("POST", fcmV1Endpoint, bytes.NewBuffer(bodyBytes))
				if err == nil {
					req.Header.Set("Content-Type", "application/json")
					resp, err := httpClient.Do(req)
					if err == nil {
						respBody, _ := io.ReadAll(resp.Body)
						_ = resp.Body.Close()
						if resp.StatusCode == http.StatusOK {
							log.Info().Int("status", resp.StatusCode).Str("chat_id", chatID).Msg("Successfully sent FCM HTTP v1 notification")
						} else {
							log.Error().Int("status", resp.StatusCode).Str("body", string(respBody)).Str("chat_id", chatID).Msg("FCM HTTP v1 error response")
						}
						continue
					} else {
						log.Error().Err(err).Msg("Error posting to FCM HTTP v1 API")
					}
				}
			}

			// Fallback: Legacy / Direct Web Push POST
			fallbackPayload := map[string]any{
				"to": tok,
				"notification": map[string]string{
					"title":        title,
					"body":         questionText,
					"icon":         "/favicon.svg",
					"click_action": targetURL,
				},
				"data": map[string]string{
					"chatID": chatID,
					"url":    targetURL,
					"title":  title,
					"body":   questionText,
				},
				"priority": "high",
			}
			bodyBytes, _ := json.Marshal(fallbackPayload)
			req, err := http.NewRequest("POST", "https://fcm.googleapis.com/fcm/send", bytes.NewBuffer(bodyBytes))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				resp, err := httpClient.Do(req)
				if err == nil {
					respBody, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					log.Info().Int("status", resp.StatusCode).Str("body", string(respBody)).Str("chat_id", chatID).Msg("Fallback FCM response")
				}
			}
		}
	}()
}
