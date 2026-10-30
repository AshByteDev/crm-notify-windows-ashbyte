package app

import "testing"

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{{"0.1.0", "0.0.9", true}, {"0.0.10", "0.0.9", true}, {"v1.0.0", "0.9.9", true}, {"0.0.3", "0.0.3", false}, {"0.0.2", "0.0.3", false}} {
		if newer(c.v, c.than) != c.want {
			t.Errorf("%s > %s", c.v, c.than)
		}
	}
}
