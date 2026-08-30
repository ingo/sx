package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("f.txt"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Alice", Email: "alice@example.com"}
	if _, err := wt.Commit("c1", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIsGitRepoAndGetRepoRootDetectFromSubdirectory pins the one behavior
// PlainOpen alone doesn't give us: `git rev-parse --is-inside-work-tree` and
// `--show-toplevel` both walk up parent directories, not just the exact
// path given — install/uninstall/mcp context detection relies on this
// working from any subdirectory of a project, not just its root.
func TestIsGitRepoAndGetRepoRootDetectFromSubdirectory(t *testing.T) {
	root := initRepo(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	if !IsGitRepo(sub) {
		t.Fatal("IsGitRepo from a subdirectory = false, want true")
	}
	got, err := GetRepoRoot(context.Background(), sub)
	if err != nil {
		t.Fatalf("GetRepoRoot: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GetRepoRoot = %q, want %q", got, want)
	}
}

func TestIsGitRepoFalseOutsideAnyRepo(t *testing.T) {
	if IsGitRepo(t.TempDir()) {
		t.Fatal("IsGitRepo on a bare temp dir = true, want false")
	}
}

func TestGetCurrentCommitAndHasUncommittedChanges(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()

	commit, err := GetCurrentCommit(ctx, root)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if len(commit) != 40 {
		t.Fatalf("GetCurrentCommit = %q, want a 40-char SHA", commit)
	}

	dirty, err := HasUncommittedChanges(ctx, root)
	if err != nil {
		t.Fatalf("HasUncommittedChanges (clean): %v", err)
	}
	if dirty {
		t.Fatal("HasUncommittedChanges on a freshly committed repo = true, want false")
	}

	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dirty, err = HasUncommittedChanges(ctx, root)
	if err != nil {
		t.Fatalf("HasUncommittedChanges (dirty): %v", err)
	}
	if !dirty {
		t.Fatal("HasUncommittedChanges after editing a tracked file = false, want true")
	}
}

func TestDetectContextForPathNotARepo(t *testing.T) {
	ctx, err := DetectContextForPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("DetectContextForPath: %v", err)
	}
	if ctx.IsRepo {
		t.Fatal("DetectContextForPath outside any repo: IsRepo = true, want false")
	}
}
