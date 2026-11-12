package sound

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWAV(t *testing.T) {
	for _, class := range []string{"client", "other", "delivery", "alert", "unknown"} {
		w := WAV(class)
		if !bytes.HasPrefix(w, []byte("RIFF")) || string(w[8:16]) != "WAVEfmt " || len(w) < 44+1000 {
			t.Fatalf("%s: not a WAV", class)
		}
		if size := binary.LittleEndian.Uint32(w[40:44]); int(size) != len(w)-44 {
			t.Fatalf("%s: data size %d of %d", class, size, len(w)-44)
		}
	}
	all := [][]byte{WAV("client"), WAV("other"), WAV("delivery"), WAV("alert")}
	for i := range all {
		for j := range i {
			if bytes.Equal(all[i], all[j]) {
				t.Fatalf("sounds %d and %d are the same", i, j)
			}
		}
	}
	if len(WAV("alert")) < 2*len(WAV("delivery")) {
		t.Fatal("alert: the sound of the delivery twice")
	}
}
