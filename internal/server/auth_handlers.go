package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	neturl "net/url"
	"path/filepath"
	"strings"

	"linkpeek/internal/auth"
	domainauth "linkpeek/internal/domain/auth"
)

// authHandlers wraps authentication HTTP handlers around the domain service.
type authHandlers struct {
	service *domainauth.Service
}

func newAuthHandlers(service *domainauth.Service) *authHandlers {
	return &authHandlers{service: service}
}

func (h *authHandlers) login(w http.ResponseWriter, r *http.Request) {
	if isTunnelHost(r.Host) {
		http.Error(w, "Login is disabled over the shared tunnel.", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		destination := sanitizeNext(r.URL.Query().Get("next"))
		if destination == "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/?next=%s", neturl.QueryEscape(destination)), http.StatusSeeOther)
	case http.MethodPost:
		token := h.service.ReadSessionToken(r)
		handleAuthFailure := func() {
			if token != "" {
				h.service.DeleteSession(token)
			}
			h.service.ClearSessionCookie(w)
		}
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			var body struct {
				Password string `json:"password"`
				Next     string `json:"next"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			next := sanitizeNext(body.Next)
			if !h.service.VerifyPassword(body.Password) {
				handleAuthFailure()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Invalid password"})
				return
			}
			mustChange := h.service.MustChangePassword()
			newToken, expires, err := h.service.CreateSession()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			h.service.SetSessionCookie(w, newToken, expires, isSecureRequest(r))
			if mustChange {
				next = "/access?must_change=1"
			}
			if next == "" {
				next = "/"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"redirect":   next,
				"expires":    expires.UTC(),
				"mustChange": mustChange,
			})
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		next := sanitizeNext(r.FormValue("next"))
		pw := r.FormValue("password")
		if !h.service.VerifyPassword(pw) {
			handleAuthFailure()
			http.Redirect(w, r, "/?login_error=1", http.StatusSeeOther)
			return
		}
		mustChange := h.service.MustChangePassword()
		if mustChange {
			next = "/access?must_change=1"
		}
		newToken, expires, err := h.service.CreateSession()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.service.SetSessionCookie(w, newToken, expires, isSecureRequest(r))
		if next == "" {
			next = "/"
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token := h.service.ReadSessionToken(r); token != "" {
		h.service.DeleteSession(token)
	}
	h.service.ClearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *authHandlers) authStatus(w http.ResponseWriter, r *http.Request) {
	token := h.service.ReadSessionToken(r)
	if ok, expires := h.service.ValidateSession(token); ok {
		h.service.SetSessionCookie(w, token, expires, isSecureRequest(r))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"expires":    expires.UTC(),
			"mustChange": h.service.MustChangePassword(),
		})
		return
	}
	if token != "" {
		h.service.DeleteSession(token)
	}
	h.service.ClearSessionCookie(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]any{"ok": false})
}

type authAccessPageData struct {
	MustChange   bool
	Requirements string
	FlashMessage string
	ErrorMessage string
	LoggedIn     bool
}

func (h *authHandlers) newAccessPageData() authAccessPageData {
	return authAccessPageData{
		MustChange:   h.service.MustChangePassword(),
		Requirements: h.service.PasswordRequirements(),
	}
}

func (h *authHandlers) renderAccessPage(w http.ResponseWriter, data authAccessPageData) {
	tpl, err := template.ParseFiles(filepath.Join("templates", "access.html"))
	if err != nil {
		log.Printf("render access page: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if err := tpl.Execute(w, data); err != nil {
		log.Printf("execute access template: %v", err)
	}
}

func (h *authHandlers) authChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := h.service.ReadSessionToken(r)
	if ok, expires := h.service.ValidateSession(token); !ok {
		if token != "" {
			h.service.DeleteSession(token)
		}
		h.service.ClearSessionCookie(w)
		h.respondChangeUnauthorized(w, r)
		return
	} else {
		h.service.SetSessionCookie(w, token, expires, isSecureRequest(r))
	}

	contentType := r.Header.Get("Content-Type")
	isJSON := strings.Contains(contentType, "application/json")
	var current, next, confirm string
	if isJSON {
		var body struct {
			Current string `json:"current"`
			Next    string `json:"next"`
			Confirm string `json:"confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			h.respondChangeError(w, r, http.StatusBadRequest, "Invalid JSON payload", true)
			return
		}
		current = strings.TrimSpace(body.Current)
		next = strings.TrimSpace(body.Next)
		confirm = strings.TrimSpace(body.Confirm)
	} else {
		if err := r.ParseForm(); err != nil {
			h.respondChangeError(w, r, http.StatusBadRequest, "Invalid form submission", false)
			return
		}
		current = strings.TrimSpace(r.FormValue("current"))
		next = strings.TrimSpace(r.FormValue("next"))
		confirm = strings.TrimSpace(r.FormValue("confirm"))
	}
	if current == "" || next == "" {
		h.respondChangeError(w, r, http.StatusBadRequest, "Current and new passwords are required", isJSON)
		return
	}
	if confirm != "" && confirm != next {
		h.respondChangeError(w, r, http.StatusBadRequest, "Passwords do not match", isJSON)
		return
	}
	if err := h.service.ChangePassword(current, next); err != nil {
		switch {
		case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordNoSpecial), errors.Is(err, auth.ErrPasswordUnchanged):
			h.respondChangeError(w, r, http.StatusBadRequest, err.Error(), isJSON)
		default:
			if strings.Contains(err.Error(), "current password is incorrect") {
				h.respondChangeError(w, r, http.StatusUnauthorized, "Current password is incorrect", isJSON)
				return
			}
			log.Printf("auth change failed: %v", err)
			h.respondChangeError(w, r, http.StatusInternalServerError, "Failed to update password", isJSON)
		}
		return
	}

	if token != "" {
		h.service.DeleteSession(token)
	}
	newToken, expires, err := h.service.CreateSession()
	if err != nil {
		h.respondChangeError(w, r, http.StatusInternalServerError, "Session unavailable", isJSON)
		return
	}
	h.service.SetSessionCookie(w, newToken, expires, isSecureRequest(r))
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"redirect":   "/access?changed=1",
			"mustChange": h.service.MustChangePassword(),
		})
		return
	}
	http.Redirect(w, r, "/access?changed=1", http.StatusSeeOther)
}

func (h *authHandlers) respondChangeUnauthorized(w http.ResponseWriter, r *http.Request) {
	acceptsJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json") || strings.Contains(r.Header.Get("Accept"), "application/json")
	if acceptsJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "Login required",
		})
		return
	}
	http.Redirect(w, r, "/?next=/access", http.StatusSeeOther)
}

func (h *authHandlers) respondChangeError(w http.ResponseWriter, r *http.Request, status int, message string, isJSON bool) {
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         false,
			"error":      message,
			"mustChange": h.service.MustChangePassword(),
		})
		return
	}
	data := h.newAccessPageData()
	data.ErrorMessage = message
	data.LoggedIn = true
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.WriteHeader(status)
	h.renderAccessPage(w, data)
}

func (h *authHandlers) accessPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := h.newAccessPageData()
	token := h.service.ReadSessionToken(r)
	ok, expires := h.service.ValidateSession(token)
	if !ok {
		if token != "" {
			h.service.DeleteSession(token)
		}
		h.service.ClearSessionCookie(w)
		http.Redirect(w, r, "/?next=/access", http.StatusSeeOther)
		return
	}
	h.service.SetSessionCookie(w, token, expires, isSecureRequest(r))
	data.LoggedIn = true
	q := r.URL.Query()
	if q.Get("must_change") == "1" {
		data.MustChange = true
	}
	if q.Get("changed") == "1" {
		data.FlashMessage = "Password updated successfully."
	}
	if msg := strings.TrimSpace(q.Get("error")); msg != "" {
		data.ErrorMessage = msg
	}
	h.renderAccessPage(w, data)
}
