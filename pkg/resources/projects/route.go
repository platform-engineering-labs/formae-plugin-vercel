// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package projects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// ResourceTypeRoute is the formae type id for a project routing rule.
const ResourceTypeRoute = "VERCEL::Projects::Route"

func init() {
	registry.Register(
		ResourceTypeRoute,
		[]resource.Operation{
			resource.OperationCreate,
			resource.OperationRead,
			resource.OperationUpdate,
			resource.OperationDelete,
			resource.OperationList,
		},
		func(c *vercelapi.Client, cfg *registry.TargetConfig) prov.Provisioner {
			return &Route{Client: c, ProjectScope: cfg.ProjectID}
		},
	)
}

// Route provisions VERCEL::Projects::Route — redirects, rewrites and status
// rules on a project.
//
// Hand-written rather than declared, for one reason: **every write only stages
// a version**. The API says so plainly — POST /v1/projects/{id}/routes
// "Stages a new version with the added route" — and a staged route is not
// serving traffic. Publishing is a second call,
// POST /v1/projects/{id}/routes/versions with `{id, action: "promote"}`.
//
// A plain rest.Definition would therefore report Success for a redirect that
// silently never takes effect, which is the worst failure mode available here:
// green apply, unchanged production. Create, Update and Delete each promote the
// version they staged.
//
// The rest of the shape does not fit the engine either: there is no item GET
// (Read scans the collection), delete is a bulk POST-like DELETE on the
// collection with `{routeIds: […]}`, and update is a whole-object PATCH on an
// item path that supports no other verb.
type Route struct {
	Client *vercelapi.Client
	// ProjectScope narrows discovery to one project, from target config.
	ProjectScope string
}

// RouteProperties is the Forma-facing shape.
type RouteProperties struct {
	ProjectID   string  `json:"projectId,omitempty"`
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	SrcSyntax   *string `json:"srcSyntax,omitempty"`
	Route       *Match  `json:"route,omitempty"`
}

// Match is the rule itself. Named Match because the wire nests `route.route`
// and two fields called Route on one struct read badly.
type Match struct {
	Src           string  `json:"src,omitempty"`
	Dest          *string `json:"dest,omitempty"`
	Status        *int    `json:"status,omitempty"`
	CaseSensitive *bool   `json:"caseSensitive,omitempty"`
}

// routeEnvelope is the request and response wrapper: `{"route": {...}}`.
type routeEnvelope struct {
	Route   *RouteProperties `json:"route"`
	Version *routeVersion    `json:"version"`
}

// routeVersion is the staged version a write produces. Its id is what promote
// needs; isLive says whether publishing already happened.
type routeVersion struct {
	ID        string `json:"id"`
	IsStaging bool   `json:"isStaging"`
	IsLive    bool   `json:"isLive"`
}

type routeList struct {
	Routes []RouteProperties `json:"routes"`
}

func routesPath(projectID string) string {
	return "/v1/projects/" + projectID + "/routes"
}

// body builds the wire shape. `projectId` and `id` live in the path and the
// native id, never in the body.
func (r *RouteProperties) body() map[string]any {
	route := map[string]any{"name": r.Name}
	if r.Description != nil {
		route["description"] = *r.Description
	}
	if r.Enabled != nil {
		route["enabled"] = *r.Enabled
	}
	if r.SrcSyntax != nil {
		route["srcSyntax"] = *r.SrcSyntax
	}
	if r.Route != nil {
		match := map[string]any{"src": r.Route.Src}
		if r.Route.Dest != nil {
			match["dest"] = *r.Route.Dest
		}
		if r.Route.Status != nil {
			match["status"] = *r.Route.Status
		}
		if r.Route.CaseSensitive != nil {
			match["caseSensitive"] = *r.Route.CaseSensitive
		}
		route["route"] = match
	}
	return map[string]any{"route": route}
}

// promote publishes a staged version. A write that staged nothing (no version
// in the response) needs no promotion; a version already live needs none
// either.
func (p *Route) promote(ctx context.Context, projectID string, v *routeVersion) error {
	if v == nil || v.ID == "" || v.IsLive {
		return nil
	}
	return p.Client.Do(ctx, vercelapi.Request{
		Method: "POST",
		Path:   routesPath(projectID) + "/versions",
		Body:   map[string]any{"id": v.ID, "action": "promote"},
	}, nil)
}

func (p *Route) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	var desired RouteProperties
	if err := json.Unmarshal(req.Properties, &desired); err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if desired.ProjectID == "" {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, "projectId is required"), nil
	}
	if desired.Name == "" || desired.Route == nil || desired.Route.Src == "" {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, "name and route.src are required"), nil
	}

	var created routeEnvelope
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "POST",
		Path:   routesPath(desired.ProjectID),
		Body:   desired.body(),
	}, &created); err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	if created.Route == nil || created.Route.ID == "" {
		return prov.FailCreate(resource.OperationErrorCodeServiceInternalError, "create response missing route id"), nil
	}
	if err := p.promote(ctx, desired.ProjectID, created.Version); err != nil {
		// The rule exists but is staged, so it is not serving traffic. Reporting
		// success here would be a lie the user only discovers in production.
		return prov.FailCreate(vercelapi.ClassifyError(err),
			fmt.Sprintf("route %s was staged but promoting it failed: %v", created.Route.ID, err)), nil
	}
	return prov.SuccessCreate(
		prov.JoinTwoPart(desired.ProjectID, created.Route.ID),
		p.declared(desired.ProjectID, created.Route),
	), nil
}

func (p *Route) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	projectID, id, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeInvalidRequest}, nil
	}
	found, err := p.find(ctx, projectID, id)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	if found == nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(p.declared(projectID, found))),
	}, nil
}

func (p *Route) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	projectID, id, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	var desired RouteProperties
	if err := json.Unmarshal(req.DesiredProperties, &desired); err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	// PATCH replaces the whole route object, so the body is built the same way
	// as create's.
	var updated routeEnvelope
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "PATCH",
		Path:   routesPath(projectID) + "/" + id,
		Body:   desired.body(),
	}, &updated); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	if err := p.promote(ctx, projectID, updated.Version); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err),
			fmt.Sprintf("route %s was updated but promoting the version failed: %v", id, err)), nil
	}
	route := updated.Route
	if route == nil {
		route = &desired
		route.ID = id
	}
	return prov.SuccessUpdate(req.NativeID, p.declared(projectID, route)), nil
}

func (p *Route) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	projectID, id, err := prov.ParseTwoPart(req.NativeID)
	if err != nil {
		return prov.FailDelete(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	// Delete is a bulk operation on the collection, with the ids in the body.
	var deleted routeEnvelope
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "DELETE",
		Path:   routesPath(projectID),
		Body:   map[string]any{"routeIds": []string{id}},
	}, &deleted); err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	if err := p.promote(ctx, projectID, deleted.Version); err != nil {
		// Without the promote the rule is removed from staging only and still
		// serves traffic in production.
		return prov.FailDelete(vercelapi.ClassifyError(err),
			fmt.Sprintf("route %s was removed from staging but promoting the version failed: %v", id, err)), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}

func (p *Route) Status(_ context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	// Promotion happens inline in the write, so there is nothing left to poll.
	return prov.SuccessStatus(req.NativeID), nil
}

func (p *Route) List(ctx context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	projectIDs, err := prov.ProjectIDs(ctx, p.Client, p.ProjectScope)
	if err != nil {
		return &resource.ListResult{NativeIDs: []string{}}, nil
	}
	nativeIDs := []string{}
	for _, projectID := range projectIDs {
		routes, err := p.collection(ctx, projectID)
		if err != nil {
			// One unreadable project must not abort discovery of the rest.
			continue
		}
		for _, route := range routes {
			if route.ID != "" {
				nativeIDs = append(nativeIDs, prov.JoinTwoPart(projectID, route.ID))
			}
		}
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// find scans the collection: there is no per-route GET.
func (p *Route) find(ctx context.Context, projectID, id string) (*RouteProperties, error) {
	routes, err := p.collection(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		if routes[i].ID == id {
			return &routes[i], nil
		}
	}
	return nil, nil
}

func (p *Route) collection(ctx context.Context, projectID string) ([]RouteProperties, error) {
	var list routeList
	if err := p.Client.Do(ctx, vercelapi.Request{
		Method: "GET",
		Path:   routesPath(projectID),
	}, &list); err != nil {
		return nil, err
	}
	return list.Routes, nil
}

// declared strips the response down to what the forma declares. The API also
// returns rawSrc, rawDest, routeType and staged, none of which a forma sets;
// reporting them would be drift on every sync.
func (p *Route) declared(projectID string, got *RouteProperties) RouteProperties {
	out := RouteProperties{
		ProjectID:   projectID,
		ID:          got.ID,
		Name:        got.Name,
		Description: got.Description,
		Enabled:     got.Enabled,
		SrcSyntax:   got.SrcSyntax,
	}
	if got.Route != nil {
		out.Route = &Match{
			Src:           got.Route.Src,
			Dest:          got.Route.Dest,
			Status:        got.Route.Status,
			CaseSensitive: got.Route.CaseSensitive,
		}
	}
	return out
}
