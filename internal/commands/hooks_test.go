package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sleuth-io/sx/v2/internal/config"
)

// A nil selection means "all detected clients" — except force-disabled ones,
// which hook installation must never touch (GitHub Copilot's hook file lands
// in the CURRENT REPO's .github/, so touching a disabled client also pollutes
// whatever repo the command runs from). The guard must hold even when the
// active profile can't be resolved (dangling defaultProfile): it reads the
// config-wide lists, not a resolved profile.
func TestInstallSelectedClientHooks_SkipsForceDisabled(t *testing.T) {
	cases := []struct {
		name           string
		defaultProfile string // "" seeds a dangling defaultProfile
	}{
		{"resolvable profile", `"defaultProfile": "default",`},
		{"dangling defaultProfile", `"defaultProfile": "ghost",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnv(t)
			workDir := env.MkdirAll(filepath.Join(env.TempDir, "work"))
			env.Chdir(workDir)

			vaultDir := env.SetupPathVault()
			env.WriteFile(
				filepath.Join(env.HomeDir, ".config", "sx", "config.json"),
				`{
  "type": "path",
  "repositoryUrl": "file://`+vaultDir+`",
  `+tc.defaultProfile+`
  "profiles": {
    "default": {"type": "path", "repositoryUrl": "file://`+vaultDir+`"}
  },
  "forceDisabledClients": ["github-copilot"]
}`,
			)

			cmd := &cobra.Command{}
			installSelectedClientHooks(context.Background(), newOutputHelper(cmd), nil)

			// Enabled client got its hooks (positive control that installation ran).
			env.AssertFileExists(filepath.Join(env.HomeDir, ".claude", "settings.json"))
			// The force-disabled client was never touched.
			env.AssertFileNotExists(filepath.Join(workDir, ".github", "hooks", "sx.json"))
		})
	}
}

// The --clients flag is an explicit selection even as "all": it rebuilds the
// config-wide enable/disable lists. Only an ABSENT flag preserves them.
func TestInitClientsFlagExplicitness(t *testing.T) {
	seedConfig := func(env *TestEnv) {
		env.WriteFile(
			filepath.Join(env.HomeDir, ".config", "sx", "config.json"),
			`{
  "type": "sleuth",
  "authToken": "tok-123",
  "repositoryUrl": "https://app.example.com",
  "defaultProfile": "local",
  "activeProfiles": ["local"],
  "profiles": {
    "local": {"type": "sleuth", "authToken": "tok-123", "repositoryUrl": "https://app.example.com"}
  },
  "forceDisabledClients": ["github-copilot"]
}`,
		)
	}
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.SetOut(nilWriter{})
		cmd.SetErr(nilWriter{})
		return cmd
	}

	t.Run("--clients all rebuilds the lists", func(t *testing.T) {
		env := NewTestEnv(t)
		env.Chdir(env.MkdirAll(filepath.Join(env.TempDir, "work")))
		seedConfig(env)
		t.Setenv("SX_PROFILE", "newprof")

		vaultDir := filepath.Join(env.TempDir, "vault-all")
		if err := runInit(newCmd(), nil, "path", "", "", vaultDir, "all"); err != nil {
			t.Fatalf("runInit --clients all: %v", err)
		}
		mpc, err := config.LoadMultiProfile()
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if len(mpc.ForceDisabledClients) != 0 {
			t.Errorf("forceDisabledClients = %v, want cleared by explicit --clients all", mpc.ForceDisabledClients)
		}
	})

	t.Run("absent flag preserves the lists", func(t *testing.T) {
		env := NewTestEnv(t)
		env.Chdir(env.MkdirAll(filepath.Join(env.TempDir, "work")))
		seedConfig(env)
		t.Setenv("SX_PROFILE", "newprof")

		vaultDir := filepath.Join(env.TempDir, "vault-noflag")
		if err := runInit(newCmd(), nil, "path", "", "", vaultDir, ""); err != nil {
			t.Fatalf("runInit without --clients: %v", err)
		}
		mpc, err := config.LoadMultiProfile()
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if got := mpc.ForceDisabledClients; len(got) != 1 || got[0] != "github-copilot" {
			t.Errorf("forceDisabledClients = %v, want preserved [github-copilot]", got)
		}
	})
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }
