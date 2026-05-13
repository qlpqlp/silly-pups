package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	pinFailuresBeforeLockout = 3
	pinLockoutDuration         = time.Hour
	pinLockoutStateFile        = "pin_lockout.json"
)

// pinLockoutError is returned when unlock is blocked after too many wrong PINs.
type pinLockoutError struct {
	Until time.Time
}

func (e *pinLockoutError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("too many incorrect PIN attempts; locked until %s UTC", e.Until.UTC().Format(time.RFC3339))
}

type pinLockoutState struct {
	ConsecutiveFailures int   `json:"consecutive_failures"`
	LockoutUntilUnix    int64 `json:"lockout_until_unix"`
}

func (s *Server) pinLockoutStatePath() string {
	return filepath.Join(s.storageDir, pinLockoutStateFile)
}

func readPINLockoutState(path string) (pinLockoutState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return pinLockoutState{}, nil
		}
		return pinLockoutState{}, err
	}
	var st pinLockoutState
	if json.Unmarshal(b, &st) != nil {
		return pinLockoutState{}, nil
	}
	return st, nil
}

func writePINLockoutState(path string, st pinLockoutState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clearPINLockoutState removes persisted PIN failure / lockout data (e.g. after successful unlock or wallet removal).
func (s *Server) clearPINLockoutState() {
	if s == nil || strings.TrimSpace(s.storageDir) == "" {
		return
	}
	_ = os.Remove(s.pinLockoutStatePath())
}

// pinLockoutCheck returns an error if unlock must be blocked until `Until`.
func (s *Server) pinLockoutCheck() error {
	if s == nil || s.storageDir == "" {
		return nil
	}
	path := s.pinLockoutStatePath()
	st, err := readPINLockoutState(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if st.LockoutUntilUnix > 0 {
		until := time.Unix(st.LockoutUntilUnix, 0).UTC()
		if now.Before(until) {
			return &pinLockoutError{Until: until}
		}
		// Lockout expired: clear wall clock; keep consecutive_failures so user does not instantly get 3 more tries without counting? Spec: "3 wrong then 1 hour" — after hour, give fresh 3 tries.
		st.LockoutUntilUnix = 0
		st.ConsecutiveFailures = 0
		_ = writePINLockoutState(path, st)
	}
	return nil
}

func (s *Server) recordPINUnlockFailure() error {
	if s == nil || strings.TrimSpace(s.storageDir) == "" {
		return nil
	}
	path := s.pinLockoutStatePath()
	st, err := readPINLockoutState(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if st.LockoutUntilUnix > 0 {
		until := time.Unix(st.LockoutUntilUnix, 0).UTC()
		if now.Before(until) {
			return &pinLockoutError{Until: until}
		}
		st.LockoutUntilUnix = 0
	}
	st.ConsecutiveFailures++
	if st.ConsecutiveFailures >= pinFailuresBeforeLockout {
		until := now.Add(pinLockoutDuration)
		st.LockoutUntilUnix = until.Unix()
		st.ConsecutiveFailures = 0
		if err := writePINLockoutState(path, st); err != nil {
			return err
		}
		return &pinLockoutError{Until: until.UTC()}
	}
	return writePINLockoutState(path, st)
}

// pinLockoutStatusForAPI returns lockout fields for /api/security/status (nil until = not locked).
func (s *Server) pinLockoutStatusForAPI() (locked bool, until *time.Time) {
	if s == nil || strings.TrimSpace(s.storageDir) == "" {
		return false, nil
	}
	st, err := readPINLockoutState(s.pinLockoutStatePath())
	if err != nil || st.LockoutUntilUnix <= 0 {
		return false, nil
	}
	t := time.Unix(st.LockoutUntilUnix, 0).UTC()
	if !time.Now().UTC().Before(t) {
		st.LockoutUntilUnix = 0
		st.ConsecutiveFailures = 0
		_ = writePINLockoutState(s.pinLockoutStatePath(), st)
		return false, nil
	}
	return true, &t
}

func writePINUnlockError(w http.ResponseWriter, err error) {
	var pl *pinLockoutError
	if errors.As(err, &pl) && pl != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":             "pin_locked",
			"pin_lockout_until": pl.Until.UTC().Format(time.RFC3339),
			"detail":            pl.Error(),
		})
		return
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
}
