package gamedata

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestIsSSHURL(t *testing.T) {
	for u, want := range map[string]bool{
		"git@github.com:fwump38/The-Hidden-Isle.git":       true,
		"ssh://git@github.com/fwump38/The-Hidden-Isle.git": true,
		"https://github.com/fwump38/The-Hidden-Isle.git":   false,
		"https://user:tok@github.com/x/y.git":              false,
		"/Users/me/The Hidden Isle":                        false,
	} {
		if IsSSHURL(u) != want {
			t.Errorf("IsSSHURL(%q) = %v", u, !want)
		}
	}
	if h := sshHost("ssh://git@example.org:2222/x.git"); h != "example.org" {
		t.Errorf("sshHost = %q", h)
	}
}

func TestDeployKeyStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "key")
	_, pub1, err := DeployKey(path)
	if err != nil {
		t.Fatal(err)
	}
	_, pub2, err := DeployKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if pub1 != pub2 || !strings.HasPrefix(pub1, "ssh-ed25519 ") {
		t.Fatalf("key changed or wrong type: %q vs %q", pub1, pub2)
	}
}

func TestGitHubHostKeysPinned(t *testing.T) {
	cb, err := hostKeyCallback("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	gh, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(githubHostKeys[0]))
	if err := cb("github.com:22", nil, gh); err != nil {
		t.Errorf("GitHub's key rejected: %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	impostor, _ := ssh.NewPublicKey(pub)
	if err := cb("github.com:22", nil, impostor); err == nil {
		t.Error("impostor host key accepted")
	}
	if _, err := hostKeyCallback("gitlab.com", ""); err == nil {
		t.Error("non-GitHub host without known_hosts should fail")
	}
}
