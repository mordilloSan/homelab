package agent

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"
)

// What a first start needs, inside the binary, so the TNAS only needs the compose file.
var (
	//go:embed config/failover.yml
	defaultConfig []byte
	//go:embed overrides/*.yml
	defaultOverrides embed.FS
)

// FirstRun writes the shipped config when there is none yet.
func FirstRun(cfgPath string) error {
	if fileExists(cfgPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	slog.Info("sem configuração: criada a por defeito (modo observe, DNS desligado)", "file", cfgPath)
	return writeAtomic(cfgPath, defaultConfig)
}

// WriteOverrides puts the shipped overrides in place when the config names
// one that is missing. An existing file is never touched.
func WriteOverrides(c *Config) {
	for _, s := range c.Services {
		dst := filepath.Join(c.Paths.OverridesDir, s.Override)
		if s.Override == "" || fileExists(dst) {
			continue
		}
		b, err := defaultOverrides.ReadFile("overrides/" + s.Override)
		if err != nil {
			slog.Warn("o override não existe", "svc", s.Name, "file", dst)
			continue
		}
		err = os.MkdirAll(c.Paths.OverridesDir, 0o755)
		if err == nil {
			err = writeAtomic(dst, b)
		}
		if err != nil {
			slog.Error("não foi possível criar o override", "svc", s.Name, "file", dst, "error", err)
			continue
		}
		slog.Info("override criado: revê os nomes do serviço e da rede", "svc", s.Name, "file", dst)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// chownLikeDir gives a file the owner of its directory: the agent runs as
// root in the container, and the files must stay editable from the TNAS.
func chownLikeDir(path string) {
	if fi, err := os.Stat(filepath.Dir(path)); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(path, int(st.Uid), int(st.Gid))
		}
	}
}

// creds is the UI login, kept in user.yml next to the config. Only the
// bcrypt hash is stored: the password is in no file and in no log.
type creds struct {
	User         string `yaml:"user"`
	PasswordHash string `yaml:"password_hash"`
	Default      bool   `yaml:"-"` // still admin/admin: the UI asks to change it
}

const defaultUser, defaultPassword = "admin", "admin"

var bcryptCost = bcrypt.DefaultCost // tests lower it: the race detector makes bcrypt slow

func newCreds(user, pw string) (*creds, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return nil, err
	}
	return &creds{User: user, PasswordHash: string(h), Default: user == defaultUser && pw == defaultPassword}, nil
}

// loadUser reads user.yml, or creates it with admin/admin.
func loadUser(path string) (*creds, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c, cerr := newCreds(defaultUser, defaultPassword)
		if cerr != nil {
			return nil, cerr
		}
		slog.Warn("sem utilizador: entra com o utilizador e a password por defeito e muda a password na interface", "file", path)
		return c, saveUser(path, c)
	}
	if err != nil {
		return nil, err
	}
	var c creds
	if err := yaml.Unmarshal(b, &c); err != nil || c.User == "" || c.PasswordHash == "" {
		return nil, fmt.Errorf("%s inválido: apaga-o para voltar ao utilizador por defeito", path)
	}
	c.Default = c.User == defaultUser && bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(defaultPassword)) == nil
	return &c, nil
}

func saveUser(path string, c *creds) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return writeAtomic(path, append([]byte("# Gerido pela interface. Só guarda o hash (bcrypt) da password.\n"), b...))
}
