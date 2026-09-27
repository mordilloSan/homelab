package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed index.html
var indexHTML []byte

// Inter, the LinuxIO typeface (SIL OFL 1.1), embedded so the page needs no internet.
//
//go:embed inter.woff2
var interFont []byte

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("GET /inter.woff2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		_, _ = w.Write(interFont)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(*a.view.Load())
	})
	mux.HandleFunc("POST /api/config", a.postConfig)
	mux.HandleFunc("POST /api/maintenance", a.postMaintenance)
	mux.HandleFunc("POST /api/action", a.postAction)
	mux.HandleFunc("POST /api/images", func(w http.ResponseWriter, _ *http.Request) {
		go a.scanImages()
		w.WriteHeader(http.StatusAccepted)
	})
	return a.auth(mux)
}

// auth: HTTP Basic with a bcrypt hash; any user name, only the password counts.
func (a *Agent) auth(next http.Handler) http.Handler {
	hash := []byte(a.cfg.UI.PasswordHash)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pw, ok := r.BasicAuth()
		if !ok || bcrypt.CompareHashAndPassword(hash, []byte(pw)) != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="failover", charset="UTF-8"`)
			http.Error(w, "password errada", http.StatusUnauthorized)
			return
		}
		// JSON only: a cross-site form cannot send it without a CORS preflight (CSRF).
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Content-Type tem de ser application/json", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		http.Error(w, "pedido inválido: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// done persists a UI change and wakes the loop so it applies without waiting.
func (a *Agent) done(w http.ResponseWriter, svc, msg string) {
	a.event(svc, msg+" (interface)")
	a.save()
	a.poke()
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) postConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode           string `json:"mode"`
		CheckIntervalS int    `json:"check_interval_s"`
		Services       []struct {
			Name         string `json:"name"`
			WaitMin      int    `json:"wait_min"`
			StabilityMin int    `json:"stability_min"`
		} `json:"services"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg
	next.Services = slices.Clone(a.cfg.Services)
	next.Mode, next.CheckIntervalS = req.Mode, req.CheckIntervalS
	for _, rs := range req.Services {
		i := slices.IndexFunc(next.Services, func(s Service) bool { return s.Name == rs.Name })
		if i < 0 {
			http.Error(w, "serviço desconhecido: "+rs.Name, http.StatusBadRequest)
			return
		}
		next.Services[i].WaitMin, next.Services[i].StabilityMin = rs.WaitMin, rs.StabilityMin
	}
	if err := next.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveConfig(a.cfgPath, &next); err != nil {
		http.Error(w, "guardar configuração: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.cfg = next
	a.now = time.Now()
	a.done(w, "", "configuração alterada")
}

func (a *Agent) postMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"` // empty = global
		Minutes int    `json:"minutes"` // 0 = off
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		http.Error(w, "minutos entre 0 e 10080", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = time.Now()
	until := &a.st.MaintUntil
	if req.Service != "" {
		if _, ok := a.service(req.Service); !ok {
			http.Error(w, "serviço desconhecido", http.StatusNotFound)
			return
		}
		until = &a.svc(req.Service).MaintUntil
	}
	*until = time.Time{}
	msg := "manutenção desligada"
	if req.Minutes > 0 {
		*until = a.now.Add(minutes(req.Minutes))
		msg = "manutenção ligada até " + until.Local().Format("02/01 15:04")
	}
	a.done(w, req.Service, msg)
}

func (a *Agent) postAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service string `json:"service"`
		Action  string `json:"action"` // failover | return
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.service(req.Service); !ok {
		http.Error(w, "serviço desconhecido", http.StatusNotFound)
		return
	}
	a.now = time.Now()
	s := a.svc(req.Service)
	switch {
	// From ERROR only once the cleanup finished, otherwise return first.
	case req.Action == "failover" && (s.State == Normal || s.State == Error && s.Snapshot == "" && !s.DNS):
		a.set(s, FailingOver)
		a.done(w, req.Service, "failover forçado")
	case req.Action == "return" && (s.State == FailingOver || s.State == Active || s.State == Error):
		a.set(s, Returning)
		a.done(w, req.Service, "regresso forçado")
	default:
		http.Error(w, "ação impossível no estado "+s.State, http.StatusConflict)
	}
}
