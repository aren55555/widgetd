package host

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Secret fields: settings a widget names in its manifest (an API key, a
// password) that are write-only. The stored config never contains them;
// they live in their own table, sealed with a key that sits beside the
// database (mode 0600), and the form only ever learns whether each is set.
// The widget sees its whole config, secrets included, at runtime.

// sealer encrypts secrets at rest.
type sealer struct {
	aead cipher.AEAD
}

func openSealer(stateDir string) (*sealer, error) {
	path := filepath.Join(stateDir, "secret.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s: want a 32-byte key", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

// seal encrypts v, bound to where it's stored (instance and field), so a
// sealed value copied to another row doesn't open.
func (s *sealer) seal(v []byte, instance, field string) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	io.ReadFull(rand.Reader, nonce)
	return s.aead.Seal(nonce, nonce, v, []byte(instance+"\x00"+field))
}

func (s *sealer) open(b []byte, instance, field string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("sealed secret too short")
	}
	return s.aead.Open(nil, b[:n], b[n:], []byte(instance+"\x00"+field))
}

// checkSecretFields refuses a manifest naming secret fields the Config
// doesn't have as plain top-level strings.
func checkSecretFields(spec protoreflect.MessageDescriptor, names []string) error {
	for _, n := range names {
		if spec == nil {
			return fmt.Errorf("secret field %q, but the widget takes no config", n)
		}
		fd := spec.Fields().ByName(protoreflect.Name(n))
		if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() || fd.IsMap() || fd.ContainingOneof() != nil {
			return fmt.Errorf("secret field %q must be a top-level string field of %s", n, spec.FullName())
		}
	}
	return nil
}

// splitSecrets returns cfg with its secret fields cleared, and their values.
func (w *loadedWidget) splitSecrets(cfg []byte) ([]byte, map[string]string, error) {
	if len(w.spec.Secrets) == 0 {
		return cfg, nil, nil
	}
	m := dynamicpb.NewMessage(w.spec.Config.ProtoReflect().Descriptor())
	if err := proto.Unmarshal(cfg, m); err != nil {
		return nil, nil, err
	}
	secrets := map[string]string{}
	for _, n := range w.spec.Secrets {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(n))
		if v := m.Get(fd).String(); v != "" {
			secrets[n] = v
		}
		m.Clear(fd)
	}
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	return out, secrets, err
}

// mergeSecrets returns cfg with each secret field that is empty in it set
// from secrets.
func (w *loadedWidget) mergeSecrets(cfg []byte, secrets map[string]string) ([]byte, error) {
	if len(w.spec.Secrets) == 0 || len(secrets) == 0 {
		return cfg, nil
	}
	m := dynamicpb.NewMessage(w.spec.Config.ProtoReflect().Descriptor())
	if err := proto.Unmarshal(cfg, m); err != nil {
		return nil, err
	}
	for _, n := range w.spec.Secrets {
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(n))
		if v, ok := secrets[n]; ok && m.Get(fd).String() == "" {
			m.Set(fd, protoreflect.ValueOfString(v))
		}
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

func (s *Server) loadSecrets(instance string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT field, value FROM secrets WHERE instance = ?`, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var field string
		var sealed []byte
		if err := rows.Scan(&field, &sealed); err != nil {
			return nil, err
		}
		v, err := s.sealer.open(sealed, instance, field)
		if err != nil {
			return nil, fmt.Errorf("secret %s of %s: %w", field, instance, err)
		}
		out[field] = string(v)
	}
	return out, rows.Err()
}

// storeSecrets writes the given secrets and removes the cleared ones.
func (s *Server) storeSecrets(tx *sql.Tx, instance string, set map[string]string, clear []string) error {
	for _, f := range clear {
		if _, err := tx.Exec(`DELETE FROM secrets WHERE instance = ? AND field = ?`, instance, f); err != nil {
			return err
		}
	}
	for f, v := range set {
		_, err := tx.Exec(`INSERT INTO secrets (instance, field, value) VALUES (?, ?, ?)
			ON CONFLICT (instance, field) DO UPDATE SET value = excluded.value`, instance, f, s.sealer.seal([]byte(v), instance, f))
		if err != nil {
			return err
		}
	}
	return nil
}

// secretsSet lists which of a widget's secret fields have a stored value.
func (s *Server) secretsSet(in *instance) []string {
	stored, err := s.loadSecrets(in.id)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range in.w.spec.Secrets {
		if _, ok := stored[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// withStoredSecrets fills the secret fields an edited config left empty
// from what's stored, except those being cleared.
func (s *Server) withStoredSecrets(in *instance, cfg []byte, clear []string) ([]byte, error) {
	if len(in.w.spec.Secrets) == 0 {
		return cfg, nil
	}
	stored, err := s.loadSecrets(in.id)
	if err != nil {
		return nil, err
	}
	for _, c := range clear {
		delete(stored, c)
	}
	return in.w.mergeSecrets(cfg, stored)
}

func validClear(w *loadedWidget, clear []string) error {
	for _, c := range clear {
		if !slices.Contains(w.spec.Secrets, c) {
			return fmt.Errorf("%q isn't a secret field", c)
		}
	}
	return nil
}
