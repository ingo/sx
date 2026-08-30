package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"

	"github.com/sleuth-io/sx/v2/internal/git"
)

// GitContext represents the current Git repository context
type GitContext struct {
	IsRepo       bool   // True if current directory is in a Git repository
	RepoRoot     string // Absolute path to repository root
	RepoURL      string // Remote repository URL
	RelativePath string // Current path relative to repo root
}

// DetectContext detects the Git context for the current working directory
func DetectContext(ctx context.Context) (*GitContext, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	return DetectContextForPath(ctx, cwd)
}

// DetectContextForPath detects the Git context for a specific path
func DetectContextForPath(ctx context.Context, path string) (*GitContext, error) {
	// Check if we're in a Git repository
	if !IsGitRepo(path) {
		return &GitContext{IsRepo: false}, nil
	}

	// Get repository root
	repoRoot, err := GetRepoRoot(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo root: %w", err)
	}

	// Get remote URL
	repoURL, err := GetRemoteURL(ctx, repoRoot)
	if err != nil {
		// Repository might not have a remote, that's okay
		repoURL = ""
	}

	// Calculate relative path
	relativePath, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate relative path: %w", err)
	}

	// Normalize to "." if at root
	if relativePath == "" {
		relativePath = "."
	}

	return &GitContext{
		IsRepo:       true,
		RepoRoot:     repoRoot,
		RepoURL:      repoURL,
		RelativePath: relativePath,
	}, nil
}

// openWithParentDetection opens the git repository containing path,
// searching parent directories the way `git rev-parse` does — plain
// PlainOpen only ever looks at path itself.
func openWithParentDetection(path string) (*gogit.Repository, error) {
	return gogit.PlainOpenWithOptions(path, &gogit.PlainOpenOptions{DetectDotGit: true})
}

// IsGitRepo checks if the given path is inside a Git repository
func IsGitRepo(path string) bool {
	_, err := openWithParentDetection(path)
	return err == nil
}

// GetRepoRoot returns the root directory of the Git repository
func GetRepoRoot(ctx context.Context, path string) (string, error) {
	repo, err := openWithParentDetection(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return wt.Filesystem.Root(), nil
}

// GetRemoteURL returns the remote URL for the repository (typically 'origin')
func GetRemoteURL(ctx context.Context, repoPath string) (string, error) {
	gitClient := git.NewClient()
	return gitClient.GetRemoteURL(ctx, repoPath)
}

// GetCurrentBranch returns the current branch name
func GetCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	gitClient := git.NewClient()
	return gitClient.GetCurrentBranch(ctx, repoPath)
}

// GetCurrentCommit returns the current commit SHA
func GetCurrentCommit(ctx context.Context, repoPath string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return head.Hash().String(), nil
}

// HasUncommittedChanges checks if there are uncommitted changes in the repository
func HasUncommittedChanges(ctx context.Context, repoPath string) (bool, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("failed to get worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return !status.IsClean(), nil
}
