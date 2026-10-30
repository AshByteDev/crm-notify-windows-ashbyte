//go:build !windows

package main

// Not Windows: a console mode for development — the cards are printed, «r <key>» reads, «o <key>» opens, «q» quits.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

func main() {
	a, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	last := ""
	a.Changed = func() {
		v := a.View()
		var b strings.Builder
		fmt.Fprintf(&b, "[%s] %s", v.State, v.User)
		if v.Error != "" {
			fmt.Fprintf(&b, " — %s", v.Error)
		}
		for _, c := range v.Queue.Visible {
			fmt.Fprintf(&b, "\n  %s | %s: %s (%d) %s", c.Key, c.Title, c.Body, c.Count, c.URL)
		}
		if v.Queue.Waiting+v.Queue.More > 0 {
			fmt.Fprintf(&b, "\n  …ещё %d (ждут %d, в CRM %d)", v.Queue.Waiting+v.Queue.More, v.Queue.Waiting, v.Queue.More)
		}
		mu.Lock()
		defer mu.Unlock()
		if s := b.String(); s != last {
			last = s
			fmt.Println(s)
		}
	}
	a.Start(ctx)
	if a.View().State == "signed_out" {
		a.Login(ctx)
	}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		f := strings.Fields(in.Text())
		switch {
		case len(f) == 1 && f[0] == "q":
			return
		case len(f) == 2 && f[0] == "r":
			a.Read(ctx, f[1])
		case len(f) == 2 && f[0] == "o":
			a.Open(f[1])
		case len(f) == 1 && f[0] == "logout":
			a.Logout(ctx)
		}
	}
}
