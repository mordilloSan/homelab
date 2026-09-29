package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var (
	pngIcon = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	svgIcon = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
)

// A dashboard-icons name given in the form (or its dashboardicons.com page)
// is downloaded from the CDN when the service is saved, kept in the state
// folder and served from there; the page never goes out for it.
func TestServiceIconLink(t *testing.T) {
	a, f := svcSetup(t)
	f.bodies = map[string][]byte{iconsCDN + "png/nextcloud-blue.png": pngIcon}
	body := strings.Replace(nextcloud, `"icon":""`, `"icon":"https://dashboardicons.com/icons/nextcloud-blue"`, 1)
	if code, got := postTo(t, a.postService, body); code != 204 {
		t.Fatalf("HTTP %d %s", code, got)
	}
	if sv, _ := a.service("nextcloud"); sv.Icon != "nextcloud-blue" {
		t.Fatalf("gravou %q, queria só o nome", sv.Icon)
	}
	if b, err := os.ReadFile(filepath.Join(a.iconDir(), "nextcloud")); err != nil || string(b) != string(pngIcon) {
		t.Fatalf("ícone guardado: %v %q", err, b)
	}
	if v := string(*a.view.Load()); !strings.Contains(v, `"icon_v":"`) {
		t.Fatal("o estado não diz que há ícone")
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, withSession(a, httptest.NewRequest(http.MethodGet, "/icons/nextcloud", nil)))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("HTTP %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/icons/vaultwarden", "/icons/..%2Fstate.json", "/icons/Nextcloud"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, withSession(a, httptest.NewRequest(http.MethodGet, p, nil)))
		if w.Code != 404 {
			t.Errorf("%s: HTTP %d", p, w.Code)
		}
	}
	if code, _ := postTo(t, a.postServiceRemove, `{"name":"nextcloud"}`); code != 204 || fileExists(filepath.Join(a.iconDir(), "nextcloud")) {
		t.Fatal("remover deixou o ícone")
	}
}

// A link anywhere else, a name the CDN does not have or an answer that is
// not an image is refused in the form; the agent never asks another host.
func TestServiceIconRefused(t *testing.T) {
	a, f := svcSetup(t)
	f.bodies = map[string][]byte{"https://example.com/nc.png": pngIcon, iconsCDN + "svg/pagina.svg": []byte("<html><body>olá</body></html>")}
	for _, icon := range []string{"https://example.com/nc.png", "cube", "pagina", "ftp://dashboardicons.com/x.png", "../state"} {
		body := strings.Replace(nextcloud, `"icon":""`, `"icon":"`+icon+`"`, 1)
		if code, got := postTo(t, a.postService, body); code != 400 || !strings.Contains(got, `"field":"icon"`) {
			t.Errorf("%s: HTTP %d %s", icon, code, got)
		}
	}
}

// Without a link, the icon of dashboard-icons by the service's name, else by
// its folder; neither there, the page's own.
func TestServiceIconGuess(t *testing.T) {
	a, f := setup(t)
	// vaultwarden by name in SVG; speedtest by its folder, only in PNG
	f.bodies = map[string][]byte{iconsCDN + "png/speedtest-tracker.png": pngIcon, iconsCDN + "svg/vaultwarden.svg": svgIcon}
	a.fetchIcons()
	if !fileExists(filepath.Join(a.iconDir(), "speedtest")) || !fileExists(filepath.Join(a.iconDir(), "vaultwarden")) {
		t.Fatal("não usou o ícone do dashboard-icons pelo nome ou pela pasta")
	}
	if fileExists(filepath.Join(a.iconDir(), "homepage")) {
		t.Fatal("guardou uma resposta que não é imagem")
	}
	f.reset()
	a.fetchIcons() // hits and misses are remembered: nothing is asked again
	if hasGet(f, iconsCDN) {
		t.Fatalf("voltou a pedir: %v", f.gets)
	}
	if got := iconType(svgIcon); got != "image/svg+xml" {
		t.Fatalf("svg: %s", got)
	}
	for _, u := range f.gets {
		if !strings.HasPrefix(u, iconsCDN) {
			t.Errorf("pediu %s", u)
		}
	}
}

func TestIconName(t *testing.T) {
	for in, want := range map[string]string{
		"speedtest-tracker": "speedtest-tracker", "Jellyfin": "jellyfin",
		"https://dashboardicons.com/icons/home-assistant":                                   "home-assistant",
		"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/speedtest-tracker.png": "speedtest-tracker",
		"https://example.com/logo.png":                                                      "", "https://cdn.jsdelivr.net/gh/outro/x.png": "", "a/b": "", "..": "",
	} {
		if got, ok := iconName(in); ok != (want != "") || ok && got != want {
			t.Errorf("%q: %q %v, queria %q", in, got, ok, want)
		}
	}
}

// The CDN down does not stop the service from being edited: the stored icon
// stays.
func TestServiceIconLinkDown(t *testing.T) {
	a, f := svcSetup(t)
	f.bodies = map[string][]byte{iconsCDN + "png/nextcloud-blue.png": pngIcon}
	body := strings.Replace(nextcloud, `"icon":""`, `"icon":"nextcloud-blue"`, 1)
	if code, got := postTo(t, a.postService, body); code != 204 {
		t.Fatalf("HTTP %d %s", code, got)
	}
	delete(f.bodies, iconsCDN+"png/nextcloud-blue.png")
	f.getErr = map[string]error{iconsCDN: errors.New("timeout")}
	edit := strings.Replace(strings.Replace(body, `"new":true,`, "", 1), `"wait_min":5`, `"wait_min":15`, 1)
	if code, got := postTo(t, a.postService, edit); code != 204 {
		t.Fatalf("editar com o link em baixo: HTTP %d %s", code, got)
	}
	if !fileExists(filepath.Join(a.iconDir(), "nextcloud")) {
		t.Fatal("o ícone guardado desapareceu")
	}
}

// A background pass started before a service was removed does not bring its
// icon back.
func TestIconPassAfterRemove(t *testing.T) {
	a, f := setup(t)
	f.bodies = map[string][]byte{iconsCDN + "svg/vaultwarden.svg": svgIcon}
	stale := slices.Clone(a.cfg.Services)
	a.cfg.Services = a.cfg.Services[1:] // vaultwarden removed meanwhile
	a.fetchIconsFrom(stale)
	if fileExists(filepath.Join(a.iconDir(), "vaultwarden")) {
		t.Fatal("o ícone de um serviço removido voltou")
	}
}

// Icon files are only ever under state/icons, whatever name reaches them.
func TestIconPathsStayInside(t *testing.T) {
	a, _ := setup(t)
	victim := filepath.Join(filepath.Dir(a.statePath), "victim")
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.dropIcon("../victim")
	a.storeIcon("../victim", nil, nil)
	if !fileExists(victim) {
		t.Fatal("apagou um ficheiro fora da pasta dos ícones")
	}
}
