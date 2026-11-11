package stickers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func picture(t *testing.T, w, h int) ([]byte, string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.SetNRGBA(1, 1, color.NRGBA{0xFF, 0, 0, 0xFF})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return b.Bytes(), hex.EncodeToString(sum[:])
}

// wait asks until the picture is there.
func wait(t *testing.T, s *Store, hash string) image.Image {
	t.Helper()
	for range 200 {
		if img := s.Get(hash, "/api/v1/desktop/stickers/"+hash+".png"); img != nil {
			return img
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no picture")
	return nil
}

func TestFetchedOnceForTheComputer(t *testing.T) {
	raw, hash := picture(t, 192, 160)
	dir := t.TempDir()
	var calls atomic.Int32
	fetch := func(_ context.Context, path string) ([]byte, error) {
		calls.Add(1)
		if path != "/api/v1/desktop/stickers/"+hash+".png" {
			t.Errorf("path %q", path)
		}
		return raw, nil
	}
	var changed atomic.Int32
	a := &Store{Dir: dir, Fetch: fetch, Changed: func() { changed.Add(1) }}
	if img := wait(t, a, hash); img.Bounds().Dx() != 192 {
		t.Fatalf("picture %v", img.Bounds())
	}
	if calls.Load() != 1 || changed.Load() != 1 {
		t.Fatalf("calls %d, changed %d", calls.Load(), changed.Load())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, hash+".png")); !bytes.Equal(got, raw) {
		t.Fatal("the file is not kept")
	}
	// another person of the computer: from the folder, no CRM
	b := &Store{Dir: dir, Fetch: func(context.Context, string) ([]byte, error) {
		t.Error("fetched again")
		return nil, errors.New("no")
	}}
	wait(t, b, hash)
	// no temporary files left
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestReplacedFileFetchedAgain(t *testing.T) {
	raw, hash := picture(t, 64, 64)
	other, _ := picture(t, 100, 64)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hash+".png"), other, 0o644); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := &Store{Dir: dir, Fetch: func(context.Context, string) ([]byte, error) { calls.Add(1); return raw, nil }}
	if img := wait(t, s, hash); img.Bounds().Dx() != 64 || calls.Load() != 1 {
		t.Fatalf("picture %v, calls %d", img.Bounds(), calls.Load())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, hash+".png")); !bytes.Equal(got, raw) {
		t.Fatal("the replaced file stays")
	}
}

func TestBadPicturesNotShown(t *testing.T) {
	raw, hash := picture(t, 64, 64)
	big, bigHash := picture(t, MaxSide+1, 10)
	cases := map[string]struct {
		hash, path string
		answer     []byte
		err        error
	}{
		"wrong sum":    {hash, "/s.png", append([]byte{}, raw[:len(raw)-1]...), nil},
		"too wide":     {bigHash, "/s.png", big, nil},
		"404":          {hash, "/s.png", nil, errors.New("HTTP 404")},
		"not a hash":   {"ABC", "/s.png", raw, nil},
		"not a path":   {hash, "https://evil/s.png", raw, nil},
		"another host": {hash, "//evil/s.png", raw, nil},
		"not png":      {hex.EncodeToString(sha256.New().Sum(nil)), "/s.png", []byte{}, nil},
	}
	for name, c := range cases {
		var calls atomic.Int32
		s := &Store{Fetch: func(context.Context, string) ([]byte, error) { calls.Add(1); return c.answer, c.err }}
		for range 20 {
			if s.Get(c.hash, c.path) != nil {
				t.Fatalf("%s: shown", name)
			}
			time.Sleep(2 * time.Millisecond)
		}
		if calls.Load() > 1 {
			t.Fatalf("%s: asked %d times, want once until the retry", name, calls.Load())
		}
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	_, hash := picture(t, 8, 8)
	now := time.Now()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	old := write(hash+".png", MaxAge+time.Hour)
	fresh := write(hash[:63]+"a.png", time.Hour)
	tmp := write(hash+"-123.tmp", 2*time.Hour)
	foreign := write("notes.txt", MaxAge*2)
	if n := Prune(dir, now); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for p, want := range map[string]bool{old: false, fresh: true, tmp: false, foreign: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: kept %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
	if Prune("", now) != 0 {
		t.Fatal("no folder")
	}
}

func TestPrepare(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "AshCRM-Notify", "stickers")
	if Prepare(dir) != dir {
		t.Fatal("not prepared")
	}
	if e, _ := os.ReadDir(dir); len(e) != 0 {
		t.Fatalf("left %v", e)
	}
	if Prepare("") != "" {
		t.Fatal("no folder")
	}
}
