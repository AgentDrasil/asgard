package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog/log"
)

// Supported OS names.
const (
	OSLinux   = "linux"
	OSWindows = "windows"
	OSMac     = "mac"
)

var validOSMap = map[string]bool{
	OSLinux:   true,
	OSWindows: true,
	OSMac:     true,
}

// KeybindingsOverrides represents OS -> ActionID -> KeybindingValue (string / []string / []any).
type KeybindingsOverrides map[string]map[string]any

// KeybindingsResponse represents the response for GET /api/keybindings.
type KeybindingsResponse struct {
	Overrides KeybindingsOverrides `json:"overrides"`
	Exists    bool                 `json:"exists"`
}

// SaveKeybindingsRequest represents the request body for PUT /api/manage/keybindings.
type SaveKeybindingsRequest struct {
	Overrides KeybindingsOverrides `json:"overrides"`
}

// keysFilePath returns the path to keys.yaml relative to the config file directory.
func (s *Server) keysFilePath() string {
	cfgPath := s.configPath
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	return filepath.Join(filepath.Dir(cfgPath), "keys.yaml")
}

// handleGetKeybindings handles GET /api/keybindings.
func (s *Server) handleGetKeybindings(w http.ResponseWriter, r *http.Request) {
	filePath := s.keysFilePath()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(KeybindingsResponse{
				Overrides: make(KeybindingsOverrides),
				Exists:    false,
			})
			return
		}
		log.Error().Err(err).Str("path", filePath).Msg("failed to read keys.yaml")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	var overrides KeybindingsOverrides
	if err := yaml.Unmarshal(data, &overrides); err != nil {
		log.Error().Err(err).Str("path", filePath).Msg("failed to parse keys.yaml")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to parse keys.yaml: %v", err)})
		return
	}

	if overrides == nil {
		overrides = make(KeybindingsOverrides)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(KeybindingsResponse{
		Overrides: overrides,
		Exists:    true,
	})
}

// Allowed modifier and base keys.
var validModifiers = map[string]string{
	"ctrl":   "Ctrl",
	"cmd":    "Cmd",
	"alt":    "Alt",
	"option": "Alt",
	"shift":  "Shift",
	"⌘":      "Cmd",
	"⌥":      "Alt",
}

var validNamedBaseKeys = map[string]string{
	"f1":           "F1",
	"f2":           "F2",
	"f3":           "F3",
	"f4":           "F4",
	"f5":           "F5",
	"f6":           "F6",
	"f7":           "F7",
	"f8":           "F8",
	"f9":           "F9",
	"f10":          "F10",
	"f11":          "F11",
	"f12":          "F12",
	"enter":        "Enter",
	"escape":       "Escape",
	"esc":          "Escape",
	"tab":          "Tab",
	"space":        "Space",
	"spacebar":     "Space",
	"backquote":    "Backquote",
	"`":            "Backquote",
	"arrowup":      "ArrowUp",
	"arrowdown":    "ArrowDown",
	"arrowleft":    "ArrowLeft",
	"arrowright":   "ArrowRight",
	"up":           "ArrowUp",
	"down":         "ArrowDown",
	"left":         "ArrowLeft",
	"right":        "ArrowRight",
	"minus":        "Minus",
	"equal":        "Equal",
	"bracketleft":  "BracketLeft",
	"bracketright": "BracketRight",
	"semicolon":    "Semicolon",
	"quote":        "Quote",
	"comma":        "Comma",
	"period":       "Period",
	"slash":        "Slash",
	"backslash":    "Backslash",
	"-":            "Minus",
	"=":            "Equal",
	"[":            "BracketLeft",
	"]":            "BracketRight",
	";":            "Semicolon",
	"'":            "Quote",
	",":            "Comma",
	".":            "Period",
	"/":            "Slash",
	"\\":           "Backslash",
}

// normalizeAndValidateToken normalizes and validates a shortcut string.
func normalizeAndValidateToken(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("empty keybinding string")
	}
	if len(trimmed) > 64 {
		return "", errors.New("keybinding string exceeds maximum length of 64 characters")
	}

	parts := strings.Split(trimmed, "+")
	var normalizedModifiers []string
	var normalizedBase string

	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			return "", fmt.Errorf("invalid token %q with empty part", raw)
		}

		lower := strings.ToLower(p)
		if mod, ok := validModifiers[p]; ok || validModifiers[lower] != "" {
			if !ok {
				mod = validModifiers[lower]
			}
			normalizedModifiers = append(normalizedModifiers, mod)
			continue
		}

		// Check if it's a valid base key
		if normalizedBase != "" {
			return "", fmt.Errorf("multiple base keys in combination %q: %s and %s", raw, normalizedBase, p)
		}

		if standard, ok := validNamedBaseKeys[lower]; ok {
			normalizedBase = standard
		} else if len(p) == 1 {
			r := rune(p[0])
			if unicode.IsLetter(r) {
				normalizedBase = strings.ToUpper(p)
			} else if unicode.IsDigit(r) {
				normalizedBase = p
			} else {
				return "", fmt.Errorf("unrecognized character %q in %q", p, raw)
			}
		} else {
			return "", fmt.Errorf("unrecognized key token %q in %q", p, raw)
		}
	}

	if normalizedBase == "" {
		return "", fmt.Errorf("no base key found in combination %q", raw)
	}

	var allTokens []string
	allTokens = append(allTokens, normalizedModifiers...)
	allTokens = append(allTokens, normalizedBase)
	return strings.Join(allTokens, "+"), nil
}

// normalizeAndValidateBinding validates a single binding value (string or []any/[]string).
// Empty slice [] means unassigned and is allowed.
func normalizeAndValidateBinding(val any) (any, error) {
	switch v := val.(type) {
	case string:
		norm, err := normalizeAndValidateToken(v)
		if err != nil {
			return nil, err
		}
		return norm, nil
	case []any:
		if len(v) == 0 {
			// Unassigned
			return []any{}, nil
		}
		var res []string
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, errors.New("keybinding array element must be a string")
			}
			norm, err := normalizeAndValidateToken(str)
			if err != nil {
				return nil, err
			}
			res = append(res, norm)
		}
		return res, nil
	case []string:
		if len(v) == 0 {
			// Unassigned
			return []string{}, nil
		}
		var res []string
		for _, str := range v {
			norm, err := normalizeAndValidateToken(str)
			if err != nil {
				return nil, err
			}
			res = append(res, norm)
		}
		return res, nil
	default:
		return nil, fmt.Errorf("invalid keybinding value type %T", val)
	}
}

// handleSaveManageKeybindings handles PUT /api/manage/keybindings.
func (s *Server) handleSaveManageKeybindings(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Limit request body size to 1MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req SaveKeybindingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// Validate OS names and tokens
	normalizedPayload := make(KeybindingsOverrides)
	for osName, actions := range req.Overrides {
		if !validOSMap[osName] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("invalid OS %q, must be linux, windows, or mac", osName)})
			return
		}

		if actions == nil {
			normalizedPayload[osName] = nil
			continue
		}

		normalizedActions := make(map[string]any)
		for actionID, binding := range actions {
			norm, err := normalizeAndValidateBinding(binding)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": fmt.Sprintf("invalid keybinding for action %q in OS %q: %v", actionID, osName, err),
				})
				return
			}
			normalizedActions[actionID] = norm
		}
		normalizedPayload[osName] = normalizedActions
	}

	s.keybindingsMu.Lock()
	defer s.keybindingsMu.Unlock()

	filePath := s.keysFilePath()
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("failed to create directory for keys.yaml")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Read existing keys.yaml if present
	existing := make(KeybindingsOverrides)
	data, err := os.ReadFile(filePath)
	if err == nil {
		if err := yaml.Unmarshal(data, &existing); err != nil {
			log.Error().Err(err).Str("path", filePath).Msg("existing keys.yaml is corrupted, rejecting PUT")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("existing keys.yaml is corrupted: %v", err),
			})
			return
		}
		if existing == nil {
			existing = make(KeybindingsOverrides)
		}
	} else if !os.IsNotExist(err) {
		log.Error().Err(err).Str("path", filePath).Msg("failed to check existing keys.yaml")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Perform segment merging
	for osName, newActions := range normalizedPayload {
		if len(newActions) == 0 {
			delete(existing, osName)
		} else {
			existing[osName] = newActions
		}
	}

	// Serialize with goccy/go-yaml
	yamlBytes, err := yaml.Marshal(existing)
	if err != nil {
		log.Error().Err(err).Msg("failed to serialize keybindings to yaml")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Atomic save via temp file
	tmpFile, err := os.CreateTemp(dir, "keys-*.tmp")
	if err == nil {
		tmpPath := tmpFile.Name()
		_, writeErr := tmpFile.Write(yamlBytes)
		syncErr := tmpFile.Sync()
		closeErr := tmpFile.Close()

		if writeErr == nil && syncErr == nil && closeErr == nil {
			renameErr := osRename(tmpPath, filePath)
			if renameErr == nil {
				writeStatusOK(w, "keybindings saved")
				return
			}

			if errors.Is(renameErr, syscall.EBUSY) || errors.Is(renameErr, syscall.EXDEV) {
				_ = os.Remove(tmpPath)
				if directErr := writeConfigDirect(filePath, string(yamlBytes)); directErr != nil {
					log.Error().Err(directErr).Str("path", filePath).Msg("failed to write keys.yaml via direct fallback")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": directErr.Error()})
					return
				}
				writeStatusOK(w, "keybindings saved")
				return
			}

			_ = os.Remove(tmpPath)
			log.Error().Err(renameErr).Str("path", filePath).Msg("failed to atomic rename keys.yaml")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": renameErr.Error()})
			return
		}

		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if directErr := writeConfigDirect(filePath, string(yamlBytes)); directErr != nil {
		log.Error().Err(directErr).Str("path", filePath).Msg("failed to write keys.yaml directly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": directErr.Error()})
		return
	}

	writeStatusOK(w, "keybindings saved")
}
