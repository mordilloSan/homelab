package agent

import (
	"cmp"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Services added, edited and removed from the UI. The name never changes:
// it is in the compose projects, the snapshots, the state and the tokens.

var validIcon = regexp.MustCompile(`^[a-z]*$`)

type mirrorDir struct {
	Dir string `json:"dir"`
}

// getMirror lists the folders of the mirror with a docker-compose.yml, the
// ones a service can be made of; the NPM's is not one.
func (a *Agent) getMirror(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	root := filepath.Join(a.cfg.Paths.MirrorSubvol, a.cfg.Paths.MirrorRoot)
	npm := a.cfg.NPM.Dir
	a.mu.Unlock()
	out := []mirrorDir{}
	ents, _ := os.ReadDir(root) // sorted by name; unreadable is an empty list
	for _, e := range ents {
		if e.IsDir() && e.Name() != npm && fileExists(filepath.Join(root, e.Name(), "docker-compose.yml")) {
			out = append(out, mirrorDir{e.Name()})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Agent) getServiceOverride(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	sv, ok := a.service(r.URL.Query().Get("name"))
	dir := a.cfg.Paths.OverridesDir
	a.mu.Unlock()
	if !ok {
		http.Error(w, "serviço desconhecido", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if sv.Override != "" {
		b, _ := os.ReadFile(filepath.Join(dir, sv.Override))
		_, _ = w.Write(b)
	}
}

type serviceReq struct {
	New           bool   `json:"new"`
	Name          string `json:"name"`
	Dir           string `json:"dir"`
	Host          string `json:"host"`
	WaitMin       int    `json:"wait_min"`
	StabilityMin  int    `json:"stability_min"`
	Icon          string `json:"icon"`
	OverrideYAML  string `json:"override_yaml"`
	RequireFreeIP string `json:"require_free_ip"`
	KumaToken     string `json:"kuma_token"` // empty keeps the current one
}

// postService adds a service (new) or edits one. What runs while the service
// is away from the server (its folder, address, override, IP) only changes
// with it in NORMAL.
//
//nolint:gocognit // one request, checked in the order the form reads
func (a *Agent) postService(w http.ResponseWriter, r *http.Request) {
	var req serviceReq
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.cfg.Services, func(s Service) bool { return s.Name == req.Name })
	switch {
	case req.New && i >= 0:
		fieldErr(w, "name", "já existe um serviço com este nome")
		return
	case !req.New && i < 0:
		http.Error(w, "serviço desconhecido", http.StatusNotFound)
		return
	}
	sv := Service{Name: req.Name, Dir: strings.TrimSpace(req.Dir), Host: strings.TrimSpace(req.Host), WaitMin: req.WaitMin,
		StabilityMin: req.StabilityMin, Icon: req.Icon, RequireFreeIP: strings.TrimSpace(req.RequireFreeIP)}
	yml := strings.TrimSpace(req.OverrideYAML)
	var old Service
	if i >= 0 {
		old = a.cfg.Services[i]
	}
	if yml != "" {
		sv.Override = cmp.Or(old.Override, sv.Name+".override.yml")
	}
	if i >= 0 && a.svc(sv.Name).State != Normal {
		cur, _ := os.ReadFile(filepath.Join(a.cfg.Paths.OverridesDir, old.Override))
		if old.Override == "" {
			cur = nil
		}
		if sv.Dir != old.Dir || sv.Host != old.Host || sv.RequireFreeIP != old.RequireFreeIP || yml != strings.TrimSpace(string(cur)) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a pasta, o endereço, o override e o IP só mudam com o serviço no servidor"})
			return
		}
	}
	next := a.cfg
	next.Services = slices.Clone(a.cfg.Services)
	if i >= 0 {
		next.Services[i] = sv
	} else {
		next.Services = append(next.Services, sv)
	}
	if err := next.validate(); err != nil {
		var fe *FieldError
		if errors.As(err, &fe) {
			fieldErr(w, fe.Field, fe.Msg)
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return
	}
	compose := filepath.Join(next.Paths.MirrorSubvol, next.Paths.MirrorRoot, sv.Dir, "docker-compose.yml")
	if !fileExists(compose) {
		fieldErr(w, "dir", "não há docker-compose.yml nesta pasta do espelho")
		return
	}
	undo := func() {}
	if yml != "" {
		var ok bool
		if undo, ok = a.writeOverride(w, next.Paths.OverridesDir, sv, compose, yml); !ok {
			return
		}
	}
	if req.KumaToken = strings.TrimSpace(req.KumaToken); req.KumaToken != "" {
		next.Kuma.ServiceTokens = maps.Clone(next.Kuma.ServiceTokens)
		if next.Kuma.ServiceTokens == nil {
			next.Kuma.ServiceTokens = map[string]string{}
		}
		next.Kuma.ServiceTokens[sv.Name] = req.KumaToken
	}
	if err := saveConfig(a.cfgPath, &next); err != nil {
		undo()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardar configuração: " + err.Error()})
		return
	}
	a.cfg = next
	a.now = time.Now()
	go a.scanImages()
	a.done(w, sv.Name, map[bool]string{true: "serviço adicionado", false: "serviço alterado"}[req.New])
}

// writeOverride checks yml as YAML and with docker compose, then puts it in
// place. undo puts back what was there, for when the config cannot be saved.
func (a *Agent) writeOverride(w http.ResponseWriter, dir string, sv Service, compose, yml string) (undo func(), ok bool) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yml), &m); err != nil || m == nil {
		msg := "tem de ser um YAML com as chaves do compose"
		if err != nil {
			msg = "YAML inválido: " + err.Error()
		}
		fieldErr(w, "override_yaml", msg)
		return nil, false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fieldErr(w, "override_yaml", "criar a pasta dos overrides: "+err.Error())
		return nil, false
	}
	final := filepath.Join(dir, sv.Override)
	tmp := filepath.Join(dir, "."+sv.Override+".check")
	if err := writeAtomic(tmp, []byte(yml+"\n")); err != nil {
		fieldErr(w, "override_yaml", "gravar o override: "+err.Error())
		return nil, false
	}
	if err := a.sys.Run("docker", "compose", "-p", "failover-"+sv.Name, "-f", compose, "-f", tmp, "config", "-q"); err != nil {
		_ = os.Remove(tmp)
		fieldErr(w, "override_yaml", "o docker compose recusou-o: "+err.Error())
		return nil, false
	}
	prev, prevErr := os.ReadFile(final)
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		fieldErr(w, "override_yaml", "gravar o override: "+err.Error())
		return nil, false
	}
	return func() {
		if prevErr == nil {
			_ = writeAtomic(final, prev)
		} else {
			_ = os.Remove(final)
		}
	}, true
}

// postServiceRemove takes a service out of the config, the state and the
// tokens. Its override file stays: it may have been written by hand.
func (a *Agent) postServiceRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.cfg.Services, func(s Service) bool { return s.Name == req.Name })
	if i < 0 {
		http.Error(w, "serviço desconhecido", http.StatusNotFound)
		return
	}
	if a.svc(req.Name).State != Normal {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "só se remove com o serviço no servidor"})
		return
	}
	next := a.cfg
	next.Services = slices.Delete(slices.Clone(a.cfg.Services), i, i+1)
	next.Kuma.ServiceTokens = maps.Clone(next.Kuma.ServiceTokens)
	delete(next.Kuma.ServiceTokens, req.Name)
	if err := saveConfig(a.cfgPath, &next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "guardar configuração: " + err.Error()})
		return
	}
	a.cfg = next
	delete(a.st.Services, req.Name)
	delete(a.beats, req.Name)
	delete(a.st.Images, req.Name)
	a.now = time.Now()
	a.done(w, req.Name, "serviço removido")
}
