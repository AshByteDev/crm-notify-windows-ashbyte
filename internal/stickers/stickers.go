// Package stickers keeps the pictures of the stickers of the cards (contract 1.11 §3.4, §5.3) in a folder shared by
// all the people of the computer: <hash>.png, where hash is the SHA-256 of the file. A file is shown only when its sum
// matches its name, so a file replaced by hand is of no effect: it is fetched again. Without a folder the pictures live
// in memory until the app ends. Codes, hashes and paths of the pictures are not logged.
package stickers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	MaxFile = 256 << 10 // the limit of the CRM for a picture
	MaxSide = 512       // a bigger picture is not drawn
	// MaxAge: a file not used for so long is removed by Prune.
	MaxAge = 30 * 24 * time.Hour
	// retry: a picture that failed is asked for again not sooner than this.
	retry = 10 * time.Minute
	// kept: the decoded pictures in memory.
	kept = 200
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash: a SHA-256 in lower-case hex, the name of a file.
func ValidHash(h string) bool { return hashRe.MatchString(h) }

// ValidPath: a path of the portal (one leading "/", no "\" — the app opens only <portal> + path).
func ValidPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.ContainsAny(p, "\\\x00")
}

// Store finds the pictures: memory, then the folder, then the CRM.
type Store struct {
	Dir string // "" — memory only
	// Fetch gets the picture from the CRM: GET <portal> + path with the key of the app.
	Fetch func(ctx context.Context, path string) ([]byte, error)
	// Changed: a picture has come; called from another goroutine.
	Changed func()
	Log     *slog.Logger

	mu     sync.Mutex
	imgs   map[string]image.Image
	order  []string // of imgs, the oldest first
	busy   map[string]bool
	failed map[string]time.Time
	now    func() time.Time
}

func (s *Store) init() {
	if s.imgs == nil {
		s.imgs, s.busy, s.failed = map[string]image.Image{}, map[string]bool{}, map[string]time.Time{}
	}
	if s.now == nil {
		s.now = time.Now
	}
}

// Get is the picture of hash; nil — not here yet (it is being loaded, it failed, the hash or the path is not valid).
// When it comes, Changed is called.
func (s *Store) Get(hash, path string) image.Image {
	if !ValidHash(hash) || !ValidPath(path) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if img, ok := s.imgs[hash]; ok {
		return img
	}
	if s.busy[hash] {
		return nil
	}
	if at, ok := s.failed[hash]; ok && s.now().Sub(at) < retry {
		return nil
	}
	s.busy[hash] = true
	go s.load(hash, path)
	return nil
}

func (s *Store) load(hash, path string) {
	img, err := s.find(hash, path)
	s.mu.Lock()
	delete(s.busy, hash)
	if err != nil {
		s.failed[hash] = s.now()
		s.mu.Unlock()
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address
		}
		s.log().Warn("sticker: not loaded", "err", err) // no hash, no path
		return
	}
	delete(s.failed, hash)
	s.imgs[hash] = img
	s.order = append(s.order, hash)
	if len(s.order) > kept {
		delete(s.imgs, s.order[0])
		s.order = s.order[1:]
	}
	s.mu.Unlock()
	if s.Changed != nil {
		s.Changed()
	}
}

var errSum = errors.New("the sum does not match")

// find reads the file of the folder, else fetches the picture and keeps it there.
func (s *Store) find(hash, path string) (image.Image, error) {
	if s.Dir != "" {
		file := filepath.Join(s.Dir, hash+".png")
		if raw, err := readSmall(file); err == nil {
			if img, err := decode(raw, hash); err == nil {
				now := s.now()
				_ = os.Chtimes(file, now, now) // in use: Prune keeps it (only the owner of the file may)
				return img, nil
			}
		}
	}
	if s.Fetch == nil {
		return nil, errors.New("no CRM")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := s.Fetch(ctx, path)
	if err != nil {
		return nil, err
	}
	img, err := decode(raw, hash)
	if err != nil {
		return nil, err
	}
	if s.Dir != "" {
		if err := keep(s.Dir, hash, raw); err != nil {
			s.log().Warn("sticker: not kept", "err", errors.Unwrap(err))
		}
	}
	return img, nil
}

func readSmall(file string) ([]byte, error) {
	st, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > MaxFile {
		return nil, errors.New("not a picture")
	}
	return os.ReadFile(file)
}

// decode checks the sum and the size before decoding.
func decode(raw []byte, hash string) (image.Image, error) {
	if len(raw) == 0 || len(raw) > MaxFile {
		return nil, errors.New("the size of the file")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, errSum
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxSide || cfg.Height > MaxSide {
		return nil, errors.New("the size of the picture")
	}
	return png.Decode(bytes.NewReader(raw))
}

// keep writes the file whole or not at all: a temporary file, then the rename. Two people writing the same picture at
// once write the same bytes.
func keep(dir, hash string, raw []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, hash+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(raw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, hash+".png"))
	}
	if err != nil {
		_ = os.Remove(tmp)
		if _, serr := os.Stat(filepath.Join(dir, hash+".png")); serr == nil {
			return nil // someone else has put it there
		}
	}
	return err
}

// Prune removes the pictures not used for MaxAge and the temporary files left by a crash. On a shared computer a
// person may remove only their own files: the rest are skipped. Only names of the store are touched.
func Prune(dir string, now time.Time) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		age := MaxAge
		switch {
		case strings.HasSuffix(name, ".png") && ValidHash(strings.TrimSuffix(name, ".png")):
		case strings.HasSuffix(name, ".tmp") && len(name) > 65 && ValidHash(name[:64]) && name[64] == '-':
			age = time.Hour
		default:
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < age {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			n++
		}
	}
	return n
}

// SharedDir is the folder of the computer: %ProgramData%\AshCRM-Notify\stickers; "" — no ProgramData.
func SharedDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		return ""
	}
	return filepath.Join(pd, "AshCRM-Notify", "stickers")
}

// Prepare creates the folder; "" — it cannot be created or written: the pictures stay in memory.
func Prepare(dir string) string {
	if dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return ""
	}
	f, err := os.CreateTemp(dir, strings.Repeat("0", 64)+"-*.tmp")
	if err != nil {
		return ""
	}
	f.Close()
	_ = os.Remove(f.Name())
	return dir
}

func (s *Store) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}
