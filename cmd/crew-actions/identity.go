package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/proc"
)

// repository returns the identity of the repository a build belongs to:
// the one repoFlag names, when it is set, and otherwise the one of the
// origin remote of the git repository run is called in, which it reads
// with git. Only the identity leaves this function, never the URL, which
// can carry a credential; neither do its errors.
func repository(ctx context.Context, repoFlag string, git proc.Runner) (model.Repository, error) {
	if repoFlag != "" {
		repo, err := model.ParseRepositoryFlag(repoFlag)
		if err != nil {
			return model.Repository{}, fmt.Errorf("--repo: %w", err)
		}
		return repo, nil
	}
	out, err := git(ctx, proc.Command{Name: "git", Args: []string{"remote", "get-url", "origin"}})
	if err != nil {
		return model.Repository{}, fmt.Errorf(
			"read the origin remote: %w; run inside a git repository with an origin, or pass --repo", err)
	}
	repo, err := model.ParseRepository(strings.TrimSpace(string(out.Stdout)))
	if err != nil {
		return model.Repository{}, fmt.Errorf("the origin remote: %w; pass --repo", err)
	}
	return repo, nil
}
