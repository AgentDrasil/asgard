package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2/google"
)

var (
	pushTokensMu sync.Mutex
	pushTokens   = make(map[string]time.Time)
)

type RegisterPushTokenRequest struct {
	Token string `json:"token"`
}

type serviceAccountFile struct {
	ProjectID string `json:"project_id"`
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

func findServiceAccountFile() (path string, data []byte, projectID string, err error) {
	candidates := []string{
		os.Getenv("FCM_SERVICE_ACCOUNT_FILE"),
		os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
		"./service-account.json",
		"./config/service-account.json",
	}

	if home, e := os.UserHomeDir(); e == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", "service-account.json"),
		)
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		b, readErr := os.ReadFile(c)
		if readErr == nil {
			var sa serviceAccountFile
			if jsonErr := json.Unmarshal(b, &sa); jsonErr == nil && sa.ProjectID != "" {
				return c, b, sa.ProjectID, nil
			}
		}
	}
	return "", nil, "", os.ErrNotExist
}

func CheckPushNotificationSetup() {
	saPath, _, projectID, err := findServiceAccountFile()
	if err != nil {
		log.Warn().Msg("FCM Service Account JSON not found! Web Push notifications will not work until service-account.json is provided (checked ~/.config/service-account.json, ./service-account.json, or FCM_SERVICE_ACCOUNT_FILE).")
	} else {
		log.Info().Str("path", saPath).Str("project_id", projectID).Msg("FCM Service Account JSON loaded successfully")
	}
}

func (s *Server) SendPushNotification(chatID string, questionText string, agentName string) {
	pushTokensMu.Lock()
	tokens := make([]string, 0, len(pushTokens))
	for t := range pushTokens {
		tokens = append(tokens, t)
	}
	pushTokensMu.Unlock()

	if len(tokens) == 0 {
		log.Debug().Msg("No registered FCM tokens to send push notification")
		return
	}

	title := "Ask User: Agent Needs Your Input"
	if agentName != "" {
		title = "Ask User (" + agentName + ")"
	}

	saPath, saData, projectID, saErr := findServiceAccountFile()
	var httpClient *http.Client

	if saErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jwtCfg, err := google.JWTConfigFromJSON(saData, "https://www.googleapis.com/auth/firebase.messaging")
		if err == nil {
			httpClient = jwtCfg.Client(ctx)
			log.Info().Str("service_account", saPath).Str("project_id", projectID).Msg("FCM HTTP v1 authenticated client initialized")
		} else {
			log.Warn().Err(err).Msg("Failed to parse Service Account JSON for FCM OAuth2")
		}
	} else {
		log.Warn().Msg("Service account JSON file not found (place 'service-account.json' in project root or set FCM_SERVICE_ACCOUNT_FILE). Falling back to direct HTTP post.")
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	targetURL := "/chat/" + chatID

	for _, token := range tokens {
		go func(tok string) {
			if projectID != "" && httpClient != nil {
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
						_ = resp.Body.Close()
						log.Info().Int("status", resp.StatusCode).Str("chat_id", chatID).Msg("Sent FCM HTTP v1 notification")
						return
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
					_ = resp.Body.Close()
				}
			}
		}(token)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
