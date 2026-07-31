package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"flexconnect/internal/appd"
	"flexconnect/internal/buildinfo"
	"flexconnect/internal/logging"
	"flexconnect/internal/types"
)

var apiserverLog = logging.WithComponent("apiserver")

const maxRequestBodyBytes = 1 << 20

type requestIDContextKey struct{}

type Server struct {
	daemon appd.Daemon
	mux    *http.ServeMux
	nextID atomic.Uint64
}

func New(daemon appd.Daemon) *Server {
	s := &Server{daemon: daemon, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strconv.FormatUint(s.nextID.Add(1), 10)
		w.Header().Set("X-FlexConnect-Request-ID", requestID)
		if r.ContentLength > maxRequestBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		s.mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("/v1/health", s.handleHealth)
	s.mux.HandleFunc("/v1/status", s.handleStatus)
	s.mux.HandleFunc("/v1/profiles", s.handleProfiles)
	s.mux.HandleFunc("/v1/profiles/", s.handleProfilesSub)
	s.mux.HandleFunc("/v1/login", s.handleLogin)
	s.mux.HandleFunc("/v1/connect", s.handleConnectCurrent)
	s.mux.HandleFunc("/v1/connect/", s.handleConnect)
	s.mux.HandleFunc("/v1/disconnect", s.handleDisconnect)
	s.mux.HandleFunc("/v1/routes/", s.handleRoutes)
	s.mux.HandleFunc("/v1/logs", s.handleLogs)
	s.mux.HandleFunc("/v1/traffic", s.handleTraffic)
	s.mux.HandleFunc("/v1/diagnostics", s.handleDiagnostics)
	s.mux.HandleFunc("/v1/watch", s.handleWatch)
	s.mux.HandleFunc("/v1/update/check", s.handleUpdateCheck)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d api_version=%s", r.Method, r.URL.Path, http.StatusOK, buildinfo.LocalAPIVersion)
	writeJSON(w, types.Health{Status: "ok", Version: buildinfo.Version, APIVersion: buildinfo.LocalAPIVersion})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodPost {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.daemon.Login(r.Context(), req); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusOK)
	writeJSON(w, s.daemon.Status())
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	switch r.Method {
	case http.MethodGet:
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusOK)
		writeJSON(w, s.daemon.ListProfiles())
	case http.MethodPut:
		var req types.ProfileUpsertRequest
		if err := decodeJSON(r, &req); err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		profile, err := s.daemon.CreateProfile(req.Profile, req.Password)
		if err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		apiserverLog.Printf("method=%s path=%s status=%d profile=%q", r.Method, r.URL.Path, http.StatusCreated, profile.ID)
		writeJSON(w, profile)
	default:
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProfilesSub(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/profiles/")
	if trimmed == "current" && r.Method == http.MethodGet {
		profile, err := s.daemon.CurrentProfile()
		if err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusNotFound, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		apiserverLog.Printf("method=%s path=%s status=%d profile=%q", r.Method, r.URL.Path, http.StatusOK, profile.ID)
		writeJSON(w, profile)
		return
	}
	if strings.HasSuffix(trimmed, "/switch") && r.Method == http.MethodPost {
		id := strings.TrimSuffix(trimmed, "/switch")
		if err := s.daemon.SwitchProfile(r.Context(), id); err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		apiserverLog.Printf("method=%s path=%s status=%d id=%q", r.Method, r.URL.Path, http.StatusNoContent, id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPut {
		var req types.ProfileUpdateRequest
		if err := decodeJSON(r, &req); err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		profile, err := s.daemon.UpdateProfile(trimmed, req)
		if err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		apiserverLog.Printf("method=%s path=%s status=%d profile=%q", r.Method, r.URL.Path, http.StatusOK, profile.ID)
		writeJSON(w, profile)
		return
	}
	if r.Method == http.MethodDelete {
		if err := s.daemon.DeleteProfile(trimmed); err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		apiserverLog.Printf("method=%s path=%s status=%d id=%q", r.Method, r.URL.Path, http.StatusNoContent, trimmed)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusNotFound)
	http.NotFound(w, r)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusOK)
	writeJSON(w, s.daemon.Logs())
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusOK)
	writeJSON(w, s.daemon.Diagnostics())
}

func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusOK)
	writeJSON(w, s.daemon.Traffic())
}

func (s *Server) handleConnectCurrent(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodPost {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.daemon.ConnectCurrent(r.Context()); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodPost {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/connect/")
	if err := s.daemon.Connect(r.Context(), id); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d id=%q", r.Method, r.URL.Path, http.StatusNoContent, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodPost {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.daemon.Disconnect(r.Context()); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodPut {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/routes/")
	var req types.RouteUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	profile, err := s.daemon.UpdateRoutes(id, req)
	if err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d profile=%q", r.Method, r.URL.Path, http.StatusOK, profile.ID)
	writeJSON(w, profile)
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiserverLog.Printf("method=%s path=%s status=%d reason=missing_flusher", r.Method, r.URL.Path, http.StatusInternalServerError)
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	apiserverLog.Printf("method=%s path=%s status=%d stream=start", r.Method, r.URL.Path, http.StatusOK)
	enc := json.NewEncoder(w)
	for notify := range s.daemon.Watch(r.Context()) {
		if err := enc.Encode(notify); err != nil {
			apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusOK, err)
			return
		}
		flusher.Flush()
	}
	apiserverLog.Printf("method=%s path=%s status=%d stream=end", r.Method, r.URL.Path, http.StatusOK)
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	logfRequest(r)
	if r.Method != http.MethodGet {
		apiserverLog.Printf("method=%s path=%s status=%d", r.Method, r.URL.Path, http.StatusMethodNotAllowed)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info, err := s.daemon.UpdateCheck(r.Context())
	if err != nil {
		apiserverLog.Printf("method=%s path=%s status=%d err=%v", r.Method, r.URL.Path, http.StatusInternalServerError, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apiserverLog.Printf("method=%s path=%s status=%d update_available=%v", r.Method, r.URL.Path, http.StatusOK, info.UpdateAvailable)
	writeJSON(w, info)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		apiserverLog.Printf("response encode failed err=%v", err)
	}
}

func decodeJSON(r *http.Request, out any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("request body exceeds %d bytes: %w", maxRequestBodyBytes, err)
		}
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing JSON data: %w", err)
	}
	return nil
}

func logfRequest(r *http.Request) {
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	apiserverLog.Printf("request_id=%s request=%s path=%s remote=%s", requestID, r.Method, r.URL.Path, r.RemoteAddr)
}
