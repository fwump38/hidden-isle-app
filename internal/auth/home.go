package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Home recognizes requests from your home network that arrive through the Cloudflare tunnel,
// so players at home can use https://<your domain> with PIN login and no SSO account.
//
// A tunnel request carries the client's real address in Cf-Connecting-IP. It counts as home
// when that address is the house's current public IPv4 (the container's own egress address,
// asked of Cloudflare every few minutes, so a changing IP is followed), or falls in a network
// the Seer marked as home (typically the house's IPv6 prefix), or in HI_HOME_NETWORKS.
type Home struct {
	file   string         // learned networks, kept in the data directory
	static []netip.Prefix // HI_HOME_NETWORKS
	trace  []string       // Cloudflare trace URLs (overridable in tests)
	client *http.Client

	mu        sync.RWMutex
	publicIPs []netip.Addr
	learned   []netip.Prefix
	checked   time.Time
	lastErr   string
}

func NewHome(file string, static []netip.Prefix) *Home {
	h := &Home{file: file, static: static, client: &http.Client{Timeout: 10 * time.Second},
		trace: []string{"https://1.1.1.1/cdn-cgi/trace", "https://[2606:4700:4700::1111]/cdn-cgi/trace"}}
	if b, err := os.ReadFile(file); err == nil {
		var ss []string
		if json.Unmarshal(b, &ss) == nil {
			for _, s := range ss {
				if p, err := netip.ParsePrefix(s); err == nil {
					h.learned = append(h.learned, p)
				}
			}
		}
	}
	return h
}

// Run refreshes the public address now and every interval until ctx ends.
func (h *Home) Run(ctx context.Context, every time.Duration) {
	for {
		h.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Refresh asks Cloudflare which public address(es) this container reaches the internet from.
// IPv6 usually fails inside Docker; that's fine, it's what "mark as home" is for.
func (h *Home) Refresh(ctx context.Context) {
	var found []netip.Addr
	var errs []string
	for _, u := range h.trace {
		a, err := h.fetch(ctx, u)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if !slices.Contains(found, a) {
			found = append(found, a)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checked = time.Now()
	if len(found) == 0 {
		h.lastErr = strings.Join(errs, "; ")
		slog.Warn("couldn't learn the home public IP; keeping the previous one", "err", h.lastErr)
		return
	}
	h.lastErr = ""
	if !slices.Equal(found, h.publicIPs) {
		slog.Info("home public address", "addrs", found)
	}
	h.publicIPs = found
}

func (h *Home) fetch(ctx context.Context, url string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "ip="); ok {
			return netip.ParseAddr(strings.TrimSpace(v))
		}
	}
	return netip.Addr{}, errors.New("no ip= line in " + url)
}

// IsHome reports whether a client address belongs to the home network.
func (h *Home) IsHome(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	h.mu.RLock()
	defer h.mu.RUnlock()
	if slices.Contains(h.publicIPs, a) {
		return true
	}
	for _, p := range append(slices.Clone(h.static), h.learned...) {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Learn marks the network of a (its /64 for IPv6, the address itself for IPv4) as home.
func (h *Home) Learn(a netip.Addr) (netip.Prefix, error) {
	a = a.Unmap()
	if !a.IsValid() {
		return netip.Prefix{}, errors.New("no client address to learn")
	}
	bits := 32
	if a.Is6() {
		bits = 64
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return netip.Prefix{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Contains(h.learned, p) {
		h.learned = append(h.learned, p)
	}
	return p, h.saveLocked()
}

// Forget removes a learned network.
func (h *Home) Forget(p netip.Prefix) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.learned = slices.DeleteFunc(h.learned, func(x netip.Prefix) bool { return x == p })
	return h.saveLocked()
}

func (h *Home) saveLocked() error {
	ss := make([]string, len(h.learned))
	for i, p := range h.learned {
		ss[i] = p.String()
	}
	b, _ := json.Marshal(ss)
	tmp := h.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, h.file)
}

// HomeStatus is shown on the admin page.
type HomeStatus struct {
	PublicIPs []netip.Addr
	Learned   []netip.Prefix
	Static    []netip.Prefix
	Checked   time.Time
	Error     string
}

func (h *Home) Status() HomeStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HomeStatus{PublicIPs: slices.Clone(h.publicIPs), Learned: slices.Clone(h.learned), Static: slices.Clone(h.static),
		Checked: h.checked, Error: h.lastErr}
}
