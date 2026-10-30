// Package sound synthesizes the two sounds of the app (no files to ship): «client» — a client wrote or a conversation
// came (two rising notes), «other» — everything else (one soft note).
package sound

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
)

const rate = 22050

type note struct {
	freq float64
	ms   int
}

var (
	once  sync.Once
	cache map[string][]byte
)

// WAV returns the sound of a class as a WAV file in memory (built once, kept for the whole run).
func WAV(class string) []byte {
	once.Do(func() {
		cache = map[string][]byte{
			"client": build([]note{{880, 130}, {1318.5, 220}}, 0.55),
			"other":  build([]note{{659.3, 200}}, 0.4),
		}
	})
	if w, ok := cache[class]; ok {
		return w
	}
	return cache["other"]
}

// build renders notes with a short attack and an exponential decay (a soft bell, no clicks).
func build(notes []note, volume float64) []byte {
	var pcm []int16
	for _, n := range notes {
		count := rate * n.ms / 1000
		for i := 0; i < count; i++ {
			t := float64(i) / rate
			env := math.Exp(-5 * t / (float64(n.ms) / 1000))
			if i < rate/200 { // 5 ms attack
				env *= float64(i) / float64(rate/200)
			}
			v := math.Sin(2*math.Pi*n.freq*t) + 0.3*math.Sin(4*math.Pi*n.freq*t)
			pcm = append(pcm, int16(v/1.3*env*volume*math.MaxInt16))
		}
	}
	var b bytes.Buffer
	data := len(pcm) * 2
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v) // fmt: PCM, mono, 22050 Hz, 16 bit
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(data))
	_ = binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}
