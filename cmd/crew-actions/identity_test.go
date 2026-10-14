package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/proc"
)

// tokenOrigin is an origin remote that carries a credential.
//
//nolint:gosec // a made-up credential, which the tests check is never kept
const tokenOrigin = "https://user:token@gitlab.com/group/sub/repo.git"

// isolateGit keeps git from reading the user's and the system's config, and
// from finding a repository above dir.
func isolateGit(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
}

// gitDir returns a new directory, outside any git repository, that the
// test works in. With init, it is a git repository, whose origin is origin
// unless origin is empty.
func gitDir(t *testing.T, init bool, origin string) string {
	t.Helper()
	dir := t.TempDir()
	isolateGit(t, dir)
	t.Chdir(dir)
	if !init {
		return dir
	}
	var g proc.Group
	commands := [][]string{{"init", "--quiet"}}
	if origin != "" {
		commands = append(commands, []string{"remote", "add", "origin", origin})
	}
	for _, args := range commands {
		if _, err := g.Run(t.Context(), proc.Command{Name: "git", Args: args}); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
	return dir
}

func TestRepositoryFromOrigin(t *testing.T) {
	gitDir(t, true, tokenOrigin)
	var g proc.Group
	got, err := repository(t.Context(), "", g.Run)
	want := model.Repository{Host: "gitlab.com", Organization: "group/sub", Name: "repo"}
	if err != nil || got != want {
		t.Fatalf("repository() = %+v, %v, want %+v", got, err, want)
	}
}

func TestRepositoryRefuses(t *testing.T) {
	tests := []struct {
		name   string
		init   bool
		origin string
	}{
		{name: "outside a git repository"},
		{name: "no origin", init: true},
		{name: "a local origin", init: true, origin: "/srv/git/repo.git"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gitDir(t, tc.init, tc.origin)
			var g proc.Group
			got, err := repository(t.Context(), "", g.Run)
			if err == nil {
				t.Fatalf("repository() = %+v, want an error", got)
			}
			if !strings.Contains(err.Error(), "--repo") {
				t.Errorf("repository() error = %q, want a hint to pass --repo", err)
			}
		})
	}
}

func TestRepositoryFromTheFlag(t *testing.T) {
	git := func(context.Context, proc.Command) (proc.Output, error) {
		t.Error("repository() called git, want --repo alone")
		return proc.Output{}, nil
	}
	for flag, want := range map[string]model.Repository{
		"gitlab.com/group/sub/repo": {Host: "gitlab.com", Organization: "group/sub", Name: "repo"},
		tokenOrigin:                 {Host: "gitlab.com", Organization: "group/sub", Name: "repo"},
	} {
		if got, err := repository(t.Context(), flag, git); err != nil || got != want {
			t.Errorf("repository(%q) = %+v, %v, want %+v", flag, got, err, want)
		}
	}
	got, err := repository(t.Context(), "gitlab.com/repo", git)
	if err == nil {
		t.Errorf("repository(gitlab.com/repo) = %+v, want an error", got)
	}
}

func TestRunOutsideGitRefuses(t *testing.T) {
	dir := gitDir(t, false, "")
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(twoSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nothing answers at the engine's address: a submit would exit 1.
	var stdout, stderr bytes.Buffer
	got := run(t.Context(), []string{"run", "--engine", "http://127.0.0.1:1", "test.yaml"}, &stdout, &stderr)
	if got != exitUsage {
		t.Fatalf("run outside git = %d, want %d; stderr %q", got, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--repo") {
		t.Errorf("stderr = %q, want a hint to pass --repo", stderr.String())
	}
}
