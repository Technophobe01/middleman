package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

// entrypointScript returns the absolute path to the production docker entrypoint,
// resolved from this test file's location so it works regardless of CWD.
func entrypointScript(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	p := filepath.Join(filepath.Dir(thisFile), "..", "..", "docker-entrypoint.sh")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("entrypoint script not found at %s: %v", p, err)
	}
	return p
}

// seedConfig runs docker-entrypoint.sh in seed-only mode with the given env and
// loads the generated config through the real loader. The returned config is
// what the server itself would see, so a bad seeded key/value fails the test
// here rather than silently breaking a container at startup.
func seedConfig(t *testing.T, home string, env map[string]string) *config.Config {
	t.Helper()
	cmd := exec.Command("sh", entrypointScript(t))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"MIDDLEMAN_SEED_ONLY=1",
		"MIDDLEMAN_HOME=" + home,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "entrypoint seed failed: %s", out)

	cfg, err := config.Load(filepath.Join(home, "config.toml"))
	require.NoErrorf(t, err, "generated config did not load")
	return cfg
}

func TestDockerEntrypointSeedsLoadableConfig(t *testing.T) {
	t.Run("defaults bind loopback with synonym allowlist", func(t *testing.T) {
		assert := assert.New(t)
		cfg := seedConfig(t, t.TempDir(), map[string]string{
			"MIDDLEMAN_PORT":          "18091",
			"MIDDLEMAN_INTERNAL_PORT": "18092",
		})
		assert.Equal("127.0.0.1", cfg.Host)
		assert.Equal(18092, cfg.Port) // the daemon binds the internal loopback port
		assert.Contains(cfg.AllowedHosts, "127.0.0.1:18091")
		assert.Contains(cfg.AllowedHosts, "localhost:18091")
		assert.False(cfg.TrustReverseProxy)
	})

	t.Run("operator allowed hosts are merged", func(t *testing.T) {
		assert := assert.New(t)
		cfg := seedConfig(t, t.TempDir(), map[string]string{
			"MIDDLEMAN_PORT":          "18091",
			"MIDDLEMAN_INTERNAL_PORT": "18092",
			"MIDDLEMAN_ALLOWED_HOSTS": "host.example:18091, other.lan",
		})
		assert.Contains(cfg.AllowedHosts, "127.0.0.1:18091") // synonyms still present
		assert.Contains(cfg.AllowedHosts, "host.example:18091")
		assert.Contains(cfg.AllowedHosts, "other.lan")
		assert.False(cfg.TrustReverseProxy)
	})

	t.Run("trust_reverse_proxy only for truthy values", func(t *testing.T) {
		truthy := []string{"1", "true", "TRUE", "yes"}
		for _, v := range truthy {
			cfg := seedConfig(t, t.TempDir(), map[string]string{
				"MIDDLEMAN_ALLOWED_HOSTS":       "proxy.example",
				"MIDDLEMAN_TRUST_REVERSE_PROXY": v,
			})
			assert.Truef(t, cfg.TrustReverseProxy, "value %q should enable trust", v)
		}
		for _, v := range []string{"", "0", "false", "no"} {
			cfg := seedConfig(t, t.TempDir(), map[string]string{
				"MIDDLEMAN_ALLOWED_HOSTS":       "proxy.example",
				"MIDDLEMAN_TRUST_REVERSE_PROXY": v,
			})
			assert.Falsef(t, cfg.TrustReverseProxy, "value %q should not enable trust", v)
		}
	})

	t.Run("hostile allowed_hosts still produce valid TOML", func(t *testing.T) {
		assert := assert.New(t)
		// Quotes, backslashes, and a newline must not break the generated TOML;
		// they are stripped, leaving bare host tokens. config.Load succeeding is
		// the core assertion (a broken file would error).
		cfg := seedConfig(t, t.TempDir(), map[string]string{
			"MIDDLEMAN_PORT":          "18091",
			"MIDDLEMAN_ALLOWED_HOSTS": "ev\"il, na\\me, line\nbreak",
		})
		assert.Contains(cfg.AllowedHosts, "evil")
		assert.Contains(cfg.AllowedHosts, "name")
		// No residual quote/backslash survived into any entry.
		for _, h := range cfg.AllowedHosts {
			assert.NotContains(h, "\"")
			assert.NotContains(h, "\\")
		}
	})

	t.Run("existing config is not overwritten", func(t *testing.T) {
		home := t.TempDir()
		sentinel := "host = \"127.0.0.1\"\nport = 9999\n"
		require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(sentinel), 0o600))
		cfg := seedConfig(t, home, map[string]string{
			"MIDDLEMAN_PORT":          "18091",
			"MIDDLEMAN_INTERNAL_PORT": "18092",
		})
		assert.Equal(t, 9999, cfg.Port, "entrypoint must not clobber an existing config")
		assert.False(t, slices.Contains(cfg.AllowedHosts, "127.0.0.1:18091"))
	})
}
