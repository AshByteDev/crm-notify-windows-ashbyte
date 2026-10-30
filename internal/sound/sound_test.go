package sound

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWAV(t *testing.T) {
	for _, class := range []string{"client", "other", "unknown"} {
		w := WAV(class)
		if !bytes.HasPrefix(w, []byte("RIFF")) || string(w[8:16]) != "WAVEfmt " || len(w) < 44+1000 {
			t.Fatalf("%s: not a WAV", class)
		}
		if size := binary.LittleEndian.Uint32(w[40:44]); int(size) != len(w)-44 {
			t.Fatalf("%s: data size %d of %d", class, size, len(w)-44)
		}
	}
	if bytes.Equal(WAV("client"), WAV("other")) {
		t.Fatal("two different sounds")
	}
}
