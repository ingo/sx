package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/spf13/cobra"

	"github.com/sleuth-io/sx/v2/internal/logger"
)

// globalSSHKeyPath stores the SSH key path for the current execution
var globalSSHKeyPath string

// SetSSHKeyPath sets the global SSH key path from either the flag or environment variable
// This should be called once at startup from the root command
func SetSSHKeyPath(cmd *cobra.Command) {
	// Priority: flag value > environment variable > empty string
	if sshKey, err := cmd.Flags().GetString("ssh-key"); err == nil && sshKey != "" {
		globalSSHKeyPath = sshKey
		printSSHKeyInfo(cmd, "flag", sshKey)
		return
	}

	// Fall back to environment variable (support both new and legacy)
	envKey := os.Getenv("SX_SSH_KEY")
	if envKey == "" {
		envKey = os.Getenv("SKILLS_SSH_KEY")
	}
	if envKey != "" {
		globalSSHKeyPath = envKey
		printSSHKeyInfo(cmd, "environment variable", envKey)
	}
}

// printSSHKeyInfo prints a safe indication that an SSH key was loaded
func printSSHKeyInfo(cmd *cobra.Command, source string, keyPathOrContent string) {
	keyPathOrContent = strings.TrimSpace(keyPathOrContent)

	var msg string
	if strings.HasPrefix(keyPathOrContent, "-----BEGIN") {
		// It's key content - show first line and length
		lines := strings.Split(keyPathOrContent, "\n")
		firstLine := strings.TrimSpace(lines[0])
		msg = fmt.Sprintf("SSH key loaded from %s (inline content, %d bytes, type: %s)\n",
			source, len(keyPathOrContent), firstLine)
	} else {
		// It's a file path - show the path
		msg = fmt.Sprintf("SSH key loaded from %s: %s\n", source, keyPathOrContent)
	}

	cmd.PrintErr(msg)
}

// GetSSHKeyPath returns the global SSH key path
func GetSSHKeyPath() string {
	return globalSSHKeyPath
}

// Client provides high-level git operations backed by go-git — an embedded,
// pure-Go implementation, not a wrapper around a system git binary. No
// external git installation is required.
type Client struct {
	sshKeyPath  string
	httpAuth    *githttp.BasicAuth
	authorName  string
	authorEmail string
}

// NewClient creates a new git client using the global SSH key path
func NewClient() *Client {
	return &Client{sshKeyPath: GetSSHKeyPath()}
}

type ClientOption func(*Client)

func NewClientWithOptions(opts ...ClientOption) *Client {
	c := NewClient()
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// WithSSHKey overrides the SSH key path (or inline PEM content) that would
// otherwise be inherited from the process-global value set by
// SetSSHKeyPath. Library consumers that don't go through the CLI flag/env
// wiring should use this to scope an SSH key to a single git.Client.
func WithSSHKey(path string) ClientOption {
	return func(c *Client) { c.sshKeyPath = path }
}

// WithCommitActor sets the author/committer identity used by Commit. When
// unset, Commit falls back to the git global config's user.name/user.email.
func WithCommitActor(name, email string) ClientOption {
	return func(c *Client) {
		c.authorName = name
		c.authorEmail = email
	}
}

func WithHTTPSBasicAuth(host, username, password string) ClientOption {
	return WithHTTPBasicAuth("https", host, username, password)
}

// WithHTTPBasicAuth configures HTTP(S) basic auth for remote operations.
// scheme/host are accepted for call-site compatibility with the pre-go-git
// API (which scoped credentials to a specific host via a git config
// extraheader) but are otherwise unused: go-git's http.BasicAuth applies to
// whichever remote a given operation targets, and every Client here only
// ever talks to the one remote it was built for.
func WithHTTPBasicAuth(scheme, host, username, password string) ClientOption {
	if host == "" || username == "" || password == "" {
		return nil
	}
	return func(c *Client) {
		c.httpAuth = &githttp.BasicAuth{Username: username, Password: password}
	}
}

// HTTPBasicAuth returns the configured HTTP basic-auth credentials, if any —
// an introspection point for tests and callers that need to confirm what
// was actually configured.
func (c *Client) HTTPBasicAuth() (username, password string, ok bool) {
	if c == nil || c.httpAuth == nil {
		return "", "", false
	}
	return c.httpAuth.Username, c.httpAuth.Password, true
}

// resolveAuth returns the transport.AuthMethod for this client's configured
// credentials — explicit HTTP basic auth takes priority; otherwise an SSH
// key, if one was configured. Neither is required (public repos need none).
func (c *Client) resolveAuth(repoURL string) (transport.AuthMethod, error) {
	if c.httpAuth != nil {
		return c.httpAuth, nil
	}
	if c.sshKeyPath == "" {
		return nil, nil
	}
	if err := ValidateSSHKey(c.sshKeyPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}
	user := "git"
	if info := ParseRemoteAuthInfo(repoURL); info.SSH {
		if u, _, ok := strings.Cut(strings.TrimPrefix(repoURL, "ssh://"), "@"); ok {
			user = u
		} else if at := strings.IndexByte(repoURL, '@'); at > 0 {
			user = repoURL[:at]
		}
	}
	if isSSHKeyContent(c.sshKeyPath) {
		keys, err := gitssh.NewPublicKeys(user, []byte(c.sshKeyPath), "")
		if err != nil {
			return nil, fmt.Errorf("invalid SSH key content: %w", err)
		}
		return keys, nil
	}
	keys, err := gitssh.NewPublicKeysFromFile(user, c.sshKeyPath, "")
	if err != nil {
		return nil, fmt.Errorf("invalid SSH key file %s: %w", c.sshKeyPath, err)
	}
	return keys, nil
}

// resolveURL converts repoURL to SSH form when an SSH key is configured and
// the URL is HTTPS — matching the pre-go-git behavior of preferring the key
// the caller explicitly provided over whatever scheme the URL happens to use.
func (c *Client) resolveURL(repoURL string) (string, error) {
	if c.sshKeyPath != "" && IsHTTPSURL(repoURL) {
		return ConvertToSSH(repoURL)
	}
	return repoURL, nil
}

// isEmptyRemoteError reports whether err is go-git's sentinel for "the
// remote has no refs at all" — an expected state for a freshly created
// vault repo, not a failure.
func isEmptyRemoteError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "remote repository is empty")
}

// Clone clones a git repository to the specified destination path. A
// genuinely empty remote (no commits, no refs) is not something go-git's
// PlainClone can produce — verified directly: it errors and leaves no .git
// behind at all — so that case falls back to init + configure the remote,
// which is the same end state `git clone` itself leaves for an empty repo.
func (c *Client) Clone(ctx context.Context, repoURL, destPath string) error {
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	url, err := c.resolveURL(repoURL)
	if err != nil {
		return err
	}
	auth, err := c.resolveAuth(repoURL)
	if err != nil {
		return err
	}

	repo, err := git.PlainCloneContext(ctx, destPath, false, &git.CloneOptions{
		URL:  url,
		Auth: auth,
	})
	if err == nil {
		return setRemoteHead(repo)
	}
	if !isEmptyRemoteError(err) {
		return classifyRemoteError(repoURL, err.Error(), err)
	}

	// Empty remote: same end state as `git clone` on an empty repo — an
	// initialized working copy with origin configured, HEAD pointing at an
	// unborn "main" (matching GitHub/GitLab's own default for new repos,
	// not go-git's PlainInit default of "master"), nothing checked out yet.
	repo, err = git.PlainInitWithOptions(destPath, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		return fmt.Errorf("failed to init empty vault clone: %w", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		return fmt.Errorf("failed to configure origin remote: %w", err)
	}
	return nil
}

// setRemoteHead records refs/remotes/origin/HEAD as a symbolic ref to the
// branch actually checked out by the clone. The git CLI always sets this up
// on a non-single-branch clone; go-git's PlainClone does not (only its
// single-branch mode does, and Clone deliberately clones every branch, not
// just one). GetDefaultBranch depends on this ref to find the remote's
// default branch on a long-lived cached clone that may since have been
// switched to some other branch.
func setRemoteHead(repo *git.Repository) error {
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to resolve cloned HEAD: %w", err)
	}
	if !head.Name().IsBranch() {
		return nil
	}
	target := plumbing.ReferenceName("refs/remotes/origin/" + head.Name().Short())
	ref := plumbing.NewSymbolicReference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), target)
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to record origin/HEAD: %w", err)
	}
	return nil
}

// Fetch fetches the origin remote. A remote with no refs at all (an empty
// vault repo nobody has pushed to yet) is not an error.
func (c *Client) Fetch(ctx context.Context, repoPath string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	remoteURL := c.remoteLocation(repo)
	auth, err := c.resolveAuth(remoteURL)
	if err != nil {
		return err
	}

	err = repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", Auth: auth})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) || isEmptyRemoteError(err) {
		return nil
	}
	return classifyRemoteError(remoteURL, err.Error(), err)
}

// resetModes maps the CLI-style mode name used by callers (mirroring the
// pre-go-git `git reset --<mode>` API) to go-git's ResetMode.
var resetModes = map[string]git.ResetMode{
	"soft":  git.SoftReset,
	"mixed": git.MixedReset,
	"hard":  git.HardReset,
}

// Reset moves HEAD (and, depending on mode, the index/worktree) to ref.
// Used by vault-clone repair to move the branch pointer back to the remote
// tip.
func (c *Client) Reset(ctx context.Context, repoPath, mode, ref string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	resetMode, ok := resetModes[mode]
	if !ok {
		return fmt.Errorf("unknown reset mode: %s", mode)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", ref, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	if err := wt.Reset(&git.ResetOptions{Mode: resetMode, Commit: *hash}); err != nil {
		return fmt.Errorf("git reset --%s %s failed: %w", mode, ref, err)
	}
	return nil
}

// RebaseAbort is a no-op: go-git has no rebase support to abort, and the
// sole caller (a v1-vault migration path predating this fork, run against
// vaults that only ever existed in go-git-managed form) already discards
// this error, treating it as best-effort cleanup either way.
func (c *Client) RebaseAbort(ctx context.Context, repoPath string) error {
	return nil
}

// IsNonFastForwardError reports whether err is go-git's sentinel for a pull
// that can't fast-forward — real divergent histories, not a network or auth
// failure. Pull only supports fast-forward merges: go-git has no 3-way merge
// and no equivalent of git's gitattributes merge drivers (e.g. the built-in
// `union` driver .sx/usage/*.jsonl relies on), so a caller that needs to
// reconcile genuine divergence must detect this and handle it itself rather
// than treating every Pull failure as fatal.
func IsNonFastForwardError(err error) bool {
	return errors.Is(err, git.ErrNonFastForwardUpdate)
}

// Pull fetches and merges origin's current branch into the worktree.
func (c *Client) Pull(ctx context.Context, repoPath string) error {
	log := logger.Get()
	start := time.Now()
	log.Debug("git pull starting", "repoPath", repoPath)

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	remoteURL := c.remoteLocation(repo)
	auth, err := c.resolveAuth(remoteURL)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	err = wt.PullContext(ctx, &git.PullOptions{RemoteName: "origin", Auth: auth})
	log.Debug("git pull completed", "duration", time.Since(start), "error", err)

	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return classifyRemoteError(remoteURL, err.Error(), err)
}

// Push pushes the current branch to origin.
func (c *Client) Push(ctx context.Context, repoPath string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	remoteURL := c.remoteLocation(repo)
	auth, err := c.resolveAuth(remoteURL)
	if err != nil {
		return err
	}
	err = repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", Auth: auth})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return classifyRemoteError(remoteURL, err.Error(), err)
}

// PushSetUpstream pushes a branch and sets its upstream tracking ref. Used
// for the first push to an empty repo and for the (uniquely named,
// never-preexisting) PR branch — neither needs --force, so this pushes
// plainly and must stay that way; force-pushing here would let a caller
// silently clobber a remote branch that already exists.
func (c *Client) PushSetUpstream(ctx context.Context, repoPath, branch string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	remoteURL := c.remoteLocation(repo)
	auth, err := c.resolveAuth(remoteURL)
	if err != nil {
		return err
	}
	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	err = repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refSpec},
		Auth:       auth,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return classifyRemoteError(remoteURL, err.Error(), err)
	}
	// Set the local tracking branch's upstream, matching `push -u`.
	branchRef := plumbing.NewBranchReferenceName(branch)
	if cfg, cerr := repo.Config(); cerr == nil {
		if cfg.Branches == nil {
			cfg.Branches = make(map[string]*config.Branch)
		}
		cfg.Branches[branch] = &config.Branch{
			Name:   branch,
			Remote: "origin",
			Merge:  branchRef,
		}
		_ = repo.SetConfig(cfg)
	}
	return nil
}

// checkoutTarget resolves ref (a branch name, tag, or commit hash) to
// CheckoutOptions — a branch reference when one exists locally or as a
// remote-tracking ref, otherwise a detached commit checkout.
//
// "HEAD" is special-cased to stay on the current branch: resolving it like
// any other ref would find the commit HEAD currently points to and check
// that out by hash, which detaches HEAD — turning "refresh the worktree to
// match HEAD" into "stop being on a branch at all". Every other ref name
// keeps its literal meaning.
func checkoutTarget(repo *git.Repository, ref string) (*git.CheckoutOptions, error) {
	if ref == "HEAD" {
		head, err := repo.Head()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve HEAD: %w", err)
		}
		if head.Name().IsBranch() {
			return &git.CheckoutOptions{Branch: head.Name()}, nil
		}
		return &git.CheckoutOptions{Hash: head.Hash()}, nil
	}
	branchRef := plumbing.NewBranchReferenceName(ref)
	if _, err := repo.Reference(branchRef, false); err == nil {
		return &git.CheckoutOptions{Branch: branchRef}, nil
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve %s: %w", ref, err)
	}
	return &git.CheckoutOptions{Hash: *hash}, nil
}

// Checkout checks out a specific ref (branch, tag, or commit)
func (c *Client) Checkout(ctx context.Context, repoPath, ref string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	opts, err := checkoutTarget(repo, ref)
	if err != nil {
		return fmt.Errorf("git checkout failed: %w", err)
	}
	if err := wt.Checkout(opts); err != nil {
		return fmt.Errorf("git checkout failed: %w", err)
	}
	return nil
}

// ForceCheckout checks out a ref with --force, restoring any missing or
// modified working-tree files. For sx-owned cache clones only: a plain
// checkout is a no-op when HEAD already equals the target, so a partial
// working tree (an interrupted delete) would otherwise never heal — and
// no local edit in these caches is worth preserving.
func (c *Client) ForceCheckout(ctx context.Context, repoPath, ref string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	opts, err := checkoutTarget(repo, ref)
	if err != nil {
		return fmt.Errorf("git checkout --force failed: %w", err)
	}
	opts.Force = true
	if err := wt.Checkout(opts); err != nil {
		return fmt.Errorf("git checkout --force failed: %w", err)
	}
	return nil
}

// HasDeletedWorktreeFiles reports whether any tracked file is missing from
// the working tree — the signature of an interrupted delete, whatever the
// repository's layout. Modified or untracked files do not count, so
// pending local changes (queued usage appends, say) never trigger a
// destructive restore.
func (c *Client) HasDeletedWorktreeFiles(ctx context.Context, repoPath string) (bool, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("failed to get worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("git ls-files --deleted failed: %w", err)
	}
	for _, s := range status {
		if s.Worktree == git.Deleted {
			return true, nil
		}
	}
	return false, nil
}

// LsRemote queries a remote repository for a specific ref
// Returns the commit hash for the ref
func (c *Client) LsRemote(ctx context.Context, repoURL, ref string) (string, error) {
	// If ref looks like a full commit hash (40 hex chars), return it directly
	if len(ref) == 40 && isHexString(ref) {
		return ref, nil
	}

	url, err := c.resolveURL(repoURL)
	if err != nil {
		return "", err
	}
	auth, err := c.resolveAuth(repoURL)
	if err != nil {
		return "", err
	}

	remote := git.NewRemote(nil, &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return "", classifyRemoteError(repoURL, err.Error(), err)
	}

	for _, want := range []plumbing.ReferenceName{
		plumbing.NewBranchReferenceName(ref),
		plumbing.NewTagReferenceName(ref),
		plumbing.ReferenceName(ref),
	} {
		for _, r := range refs {
			if r.Name() == want {
				return r.Hash().String(), nil
			}
		}
	}
	return "", fmt.Errorf("ref not found: %s", ref)
}

// RevParse resolves a ref to a commit hash in a local repository
func (c *Client) RevParse(ctx context.Context, repoPath, ref string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return hash.String(), nil
}

// HasCommit reports whether the commit exists in the local repository,
// without touching the network.
func (c *Client) HasCommit(ctx context.Context, repoPath, sha string) bool {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false
	}
	_, err = repo.CommitObject(plumbing.NewHash(sha))
	return err == nil
}

// IsRepo reports whether repoPath is a usable git repository. PlainOpen
// requires .git directly inside repoPath — no upward discovery — so an
// ancestor repository (a cache dir under a dotfiles-managed $HOME, say)
// cannot make a corrupt cache look healthy.
func (c *Client) IsRepo(ctx context.Context, repoPath string) bool {
	_, err := git.PlainOpen(repoPath)
	return err == nil
}

// GetRemoteURL returns the remote URL for the repository (typically 'origin')
func (c *Client) GetRemoteURL(ctx context.Context, repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return "", fmt.Errorf("git remote get-url failed: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", errors.New("origin remote has no URL configured")
	}
	return urls[0], nil
}

// GetCurrentBranch returns the current branch name
func (c *Client) GetCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return head.Name().Short(), nil
}

// GetCurrentBranchSymbolic returns the current branch name by reading HEAD's
// symbolic target directly, which works even on empty repos (no commits
// yet) — repo.Head() requires HEAD to resolve to a commit and fails there.
func (c *Client) GetCurrentBranchSymbolic(ctx context.Context, repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	ref, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref failed: %w", err)
	}
	return ref.Target().Short(), nil
}

// GetDefaultBranch returns the remote's default branch (e.g. "main") by
// reading the origin/HEAD symbolic ref that a clone records. Use this
// instead of the clone's current HEAD when you need the repo's real base
// branch: the cached clone is long-lived and may be left checked out on
// some other branch.
func (c *Client) GetDefaultBranch(ctx context.Context, repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}
	ref, err := repo.Reference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), false)
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref refs/remotes/origin/HEAD failed: %w", err)
	}
	return strings.TrimPrefix(ref.Target().Short(), "origin/"), nil
}

// CheckoutNewBranch creates and switches to a new branch
func (c *Client) CheckoutNewBranch(ctx context.Context, repoPath, branch string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	branchRef := plumbing.NewBranchReferenceName(branch)
	if _, err := repo.Reference(branchRef, false); err == nil {
		return fmt.Errorf("git checkout -b failed: branch %s already exists", branch)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: branchRef, Create: true}); err != nil {
		return fmt.Errorf("git checkout -b failed: %w", err)
	}
	return nil
}

// CheckoutNewBranchForce creates (or resets, if it already exists) a branch at
// the current HEAD and switches to it. Unlike CheckoutNewBranch it does not fail
// when the branch name is already present locally — important for the cached
// vault clone, which is reused across runs and may carry a leftover branch from
// a previous attempt.
func (c *Client) CheckoutNewBranchForce(ctx context.Context, repoPath, branch string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	branchRef := plumbing.NewBranchReferenceName(branch)
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("git checkout -B failed: %w", err)
	}
	// Move (or create) the branch ref to HEAD first — Checkout with
	// Create:true refuses an existing ref, and without Create it would
	// leave a pre-existing branch wherever it last pointed.
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branchRef, head.Hash())); err != nil {
		return fmt.Errorf("git checkout -B failed: %w", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{Branch: branchRef, Force: true}); err != nil {
		return fmt.Errorf("git checkout -B failed: %w", err)
	}
	return nil
}

// Add stages files for commit. A single "." stages everything, matching
// `git add .`; go-git's Worktree.Add expects individual paths, so that case
// goes through AddWithOptions{All: true} instead.
func (c *Client) Add(ctx context.Context, repoPath string, paths ...string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	if len(paths) == 1 && paths[0] == "." {
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return fmt.Errorf("git add failed: %w", err)
		}
		return nil
	}
	for _, p := range paths {
		if _, err := wt.Add(p); err != nil {
			return fmt.Errorf("git add failed: %w", err)
		}
	}
	return nil
}

// commitSignature resolves the author/committer identity: the client's
// configured actor (WithCommitActor), falling back to the git global
// config's user.name/user.email the same way the git CLI would.
func (c *Client) commitSignature() (*object.Signature, error) {
	name, email := c.authorName, c.authorEmail
	if name == "" || email == "" {
		if cfg, err := config.LoadConfig(config.GlobalScope); err == nil {
			if name == "" {
				name = cfg.User.Name
			}
			if email == "" {
				email = cfg.User.Email
			}
		}
	}
	if name == "" || email == "" {
		return nil, errors.New("no commit identity configured: set a name/email (WithCommitActor) or git's global user.name/user.email")
	}
	return &object.Signature{Name: name, Email: email, When: time.Now()}, nil
}

// Commit creates a commit with the given message
func (c *Client) Commit(ctx context.Context, repoPath, message string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	sig, err := c.commitSignature()
	if err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}
	if _, err := wt.Commit(message, &git.CommitOptions{Author: sig}); err != nil {
		return fmt.Errorf("git commit failed: %w", err)
	}
	return nil
}

// IsEmpty checks if a repository has no commits (e.g., freshly cloned empty repo)
func (c *Client) IsEmpty(ctx context.Context, repoPath string) (bool, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("not a git repository: %s", repoPath)
	}
	_, err = repo.Head()
	if err == nil {
		return false, nil
	}
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return true, nil
	}
	return false, fmt.Errorf("git rev-parse HEAD failed: %w", err)
}

// HasStagedChanges checks if there are staged changes ready to be committed
func (c *Client) HasStagedChanges(ctx context.Context, repoPath string) (bool, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("failed to open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("failed to get worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("git diff failed: %w", err)
	}
	for _, s := range status {
		if s.Staging != git.Unmodified {
			return true, nil
		}
	}
	return false, nil
}

// remoteLocation returns the best human-readable identifier for a repo in
// error messages: prefer the origin URL (what the user typed/cloned from),
// fall back to a generic label. Used by Fetch/Pull/Push so classified errors
// mention something the user recognizes, not a cache-dir hash.
func (c *Client) remoteLocation(repo *git.Repository) string {
	if remote, err := repo.Remote("origin"); err == nil {
		if urls := remote.Config().URLs; len(urls) > 0 {
			return urls[0]
		}
	}
	return "origin"
}

// classifyRemoteError turns a go-git error into an actionable one. It
// distinguishes "repo not found" from "auth required" so the caller can show
// a useful next-step hint instead of dumping a raw error.
func classifyRemoteError(repoURL, output string, err error) error {
	authHint := "To authenticate:\n" +
		"  - For private repos over HTTPS: pass a token via WithHTTPBasicAuth\n" +
		"  - Or use an SSH URL like git@github.com:owner/repo.git\n" +
		"    with --ssh-key /path/to/key"

	lc := strings.ToLower(output)
	switch {
	case strings.Contains(lc, "authentication required"),
		strings.Contains(lc, "authorization failed"),
		strings.Contains(lc, "invalid credentials"),
		strings.Contains(lc, "permission denied (publickey)"):
		return fmt.Errorf("authentication required for %s\nThe repository may be private, or you may not have access.\n%s", repoURL, authHint)
	case strings.Contains(lc, "repository not found"),
		strings.Contains(lc, "not found"):
		return fmt.Errorf("repository not found: %s\nCheck the URL is correct. If it's a private repo, this can also mean you lack access:\n%s", repoURL, authHint)
	case strings.Contains(lc, "no such host"),
		strings.Contains(lc, "network is unreachable"),
		strings.Contains(lc, "connection refused"),
		strings.Contains(lc, "connection timed out"),
		strings.Contains(lc, "i/o timeout"):
		return fmt.Errorf("network error reaching %s: %s", repoURL, strings.TrimSpace(output))
	}
	if output == "" {
		return fmt.Errorf("git operation failed for %s: %w", repoURL, err)
	}
	return fmt.Errorf("git operation failed for %s: %w\nOutput: %s", repoURL, err, output)
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	const hexChars = "0123456789abcdefABCDEF"
	for _, c := range s {
		if !strings.ContainsRune(hexChars, c) {
			return false
		}
	}
	return true
}
