package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestRepoOperationsDoNotEscapeToAncestor guards the property the exec-based
// implementation needed GIT_CEILING_DIRECTORIES for: a corrupt nested cache
// dir (.git exists but is empty) must never silently resolve to an
// ancestor's real repository. go-git's PlainOpen takes repoPath literally
// with no upward discovery — confirmed directly — so this is now structural
// rather than something each method has to defend individually; this test
// exists to catch a regression if that ever changes.
func TestRepoOperationsDoNotEscapeToAncestor(t *testing.T) {
	ancestor := t.TempDir()
	repo, err := git.PlainInit(ancestor, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ancestor, "f.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("f.txt"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Alice", Email: "alice@example.com", When: time.Now()}
	if _, err := wt.Commit("c1", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
	ancestorHead, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	ancestorSHA := ancestorHead.Hash().String()

	// A corrupt cache dir inside the ancestor: .git exists but is empty.
	cache := filepath.Join(ancestor, "cache", "git-repos", "hash")
	if err := os.MkdirAll(filepath.Join(cache, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	client := NewClient()
	ctx := context.Background()

	if client.IsRepo(ctx, cache) {
		t.Fatal("IsRepo on a corrupt cache dir must not resolve to the ancestor repository")
	}
	if err := client.Fetch(ctx, cache); err == nil {
		t.Fatal("Fetch on a corrupt cache dir resolved to the ancestor repository")
	}
	if err := client.Checkout(ctx, cache, ancestorSHA); err == nil {
		t.Fatal("Checkout on a corrupt cache dir resolved to the ancestor repository")
	}
	if client.HasCommit(ctx, cache, ancestorSHA) {
		t.Fatal("HasCommit on a corrupt cache dir resolved to the ancestor repository")
	}

	// The ancestor must be completely untouched.
	head, err := repo.Head()
	if err != nil || head.Hash().String() != ancestorSHA {
		t.Fatalf("ancestor HEAD changed: %q -> %v, %v", ancestorSHA, head, err)
	}
}
