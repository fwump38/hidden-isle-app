package gamedata

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// githubHostKeys are GitHub's published SSH host keys (https://api.github.com/meta, "ssh_keys").
// Pinning them means a sync can't be redirected to an impostor even on first connect.
var githubHostKeys = []string{
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
	"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=",
	"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=",
}

// IsSSHURL reports whether a repo URL uses SSH (git@host:owner/repo.git or ssh://…).
func IsSSHURL(u string) bool {
	if strings.HasPrefix(u, "ssh://") {
		return true
	}
	at, colon := strings.Index(u, "@"), strings.Index(u, ":")
	return !strings.Contains(u, "://") && at > 0 && colon > at
}

// sshHost returns the host part of an SSH repo URL.
func sshHost(u string) string {
	u = strings.TrimPrefix(u, "ssh://")
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexAny(u, ":/"); i >= 0 {
		u = u[:i]
	}
	return u
}

// DeployKey loads the SSH key the app uses to read the rules repo, creating an ed25519 key on
// first use. Add the public half to the repo as a read-only deploy key.
func DeployKey(path string) (signer ssh.Signer, authorizedKey string, err error) {
	pemBytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, "", genErr
		}
		block, mErr := ssh.MarshalPrivateKey(priv, "hidden-isle-app")
		if mErr != nil {
			return nil, "", mErr
		}
		pemBytes = pem.EncodeToMemory(block)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, "", err
		}
		if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	signer, err = ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, "", fmt.Errorf("deploy key %s: %w", path, err)
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return signer, pub + " hidden-isle-app", nil
}

// hostKeyCallback verifies the server: GitHub's pinned keys for github.com, otherwise the
// known_hosts file given in GAMEDATA_KNOWN_HOSTS.
func hostKeyCallback(host, knownHostsFile string) (ssh.HostKeyCallback, error) {
	if knownHostsFile != "" {
		return knownhosts.New(knownHostsFile)
	}
	if host != "github.com" {
		return nil, fmt.Errorf("unknown SSH host %q: set GAMEDATA_KNOWN_HOSTS to a known_hosts file for it", host)
	}
	var pinned [][]byte
	for _, line := range githubHostKeys {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, k.Marshal())
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		got := key.Marshal()
		for _, p := range pinned {
			if bytes.Equal(p, got) {
				return nil
			}
		}
		return fmt.Errorf("host key for %s doesn't match GitHub's published keys", hostname)
	}, nil
}

func sshAuth(repoURL, keyPath, knownHostsFile string) (*gitssh.PublicKeys, error) {
	signer, _, err := DeployKey(keyPath)
	if err != nil {
		return nil, err
	}
	cb, err := hostKeyCallback(sshHost(repoURL), knownHostsFile)
	if err != nil {
		return nil, err
	}
	auth := &gitssh.PublicKeys{User: "git", Signer: signer}
	auth.HostKeyCallback = cb
	return auth, nil
}
