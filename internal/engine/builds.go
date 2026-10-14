package engine

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/spec"
)

// Submit creates a build of the request's repository from its workflow
// file, and returns the build's id. The workflow is named after the file,
// and the file is compiled once, here: the build stores the compiled
// workflow as its definition (KTD3). An empty part of the repository's
// identity, a file name that is no workflow file's, or a file spec refuses,
// such as one with a key crew actions does not run yet, gives an error
// matching model.ErrInvalid with the refusal's message, and creates
// nothing.
func (c *Clients) Submit(ctx context.Context, req *api.SubmitRequest) (*api.SubmitResponse, error) {
	e := c.engine
	repo := model.Repository{Host: req.GetHost(), Organization: req.GetOrganization(), Name: req.GetRepository()}
	if repo.Host == "" || repo.Organization == "" || repo.Name == "" {
		return nil, fmt.Errorf("submit a build: %w",
			model.Invalid("a build needs its repository's host, organization and name"))
	}
	name, err := spec.NameFromPath(req.GetFileName())
	if err != nil {
		return nil, fmt.Errorf("submit a build: %w", model.Invalid(err.Error()))
	}
	w, err := spec.Parse(name, req.GetContent())
	if err != nil {
		return nil, fmt.Errorf("submit %s: %w", req.GetFileName(), model.Invalid(err.Error()))
	}
	definition, err := proto.Marshal(compile(w))
	if err != nil {
		return nil, fmt.Errorf("submit %s: encode the workflow: %w", req.GetFileName(), err)
	}
	id, err := e.store.CreateBuild(ctx, repo, []model.NewWorkflow{{Name: name, Definition: definition}})
	if err != nil {
		return nil, fmt.Errorf("submit %s: %w", req.GetFileName(), err)
	}
	e.notify.broadcast()
	return &api.SubmitResponse{BuildId: int64(id)}, nil
}

// compile returns the workflow as the runner receives it.
func compile(w spec.Workflow) *protocol.Workflow {
	steps := make([]*protocol.Step, len(w.Steps))
	for i, s := range w.Steps {
		steps[i] = &protocol.Step{Name: s.Name, Commands: s.Commands}
	}
	return &protocol.Workflow{Name: w.Name, Steps: steps}
}
