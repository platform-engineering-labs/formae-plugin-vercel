// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Resource executes a Definition against the Vercel API.
type Resource struct {
	def    Definition
	client *vercelapi.Client
	// projectScope optionally restricts List to one project (target config
	// `projectId`).
	projectScope string
}

var _ prov.Provisioner = (*Resource)(nil)

// New binds a Definition to a live client.
func New(def Definition, client *vercelapi.Client, projectScope string) *Resource {
	return &Resource{def: def, client: client, projectScope: projectScope}
}

// props is the untyped property document. Resources are declared in PKL, so
// the plugin only has to filter and forward — fifty Go structs would buy
// nothing.
type props map[string]any

// splitNativeID returns (parent, id). Account-scoped resources have no parent.
func (r *Resource) splitNativeID(nativeID string) (parent, id string, err error) {
	if r.def.Scope == ScopeAccount {
		if nativeID == "" {
			return "", "", fmt.Errorf("native id must not be empty")
		}
		return "", nativeID, nil
	}
	parts := strings.SplitN(nativeID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("native id must be {%s}/{id}, got %q", r.def.ParentProperty, nativeID)
	}
	return parts[0], parts[1], nil
}

func (r *Resource) joinNativeID(parent, id string) string {
	if r.def.Scope == ScopeAccount {
		return id
	}
	return parent + "/" + id
}

// path fills a template's {parent} and {id} placeholders.
func path(template, parent, id string) string {
	out := strings.ReplaceAll(template, "{parent}", parent)
	return strings.ReplaceAll(out, "{id}", id)
}

// body builds a request payload from the declared fields. The parent property
// is excluded: it is a path segment, not a body field.
func (r *Resource) body(p props, forUpdate bool) map[string]any {
	out := make(map[string]any, len(r.def.Fields))
	for _, field := range r.def.Fields {
		if forUpdate && r.def.isCreateOnly(field) {
			continue
		}
		if v, ok := p[field]; ok {
			out[r.def.apiName(field)] = v
		}
	}
	return out
}

// toProperties narrows an API response to the declared fields, then puts back
// the id and (for scoped resources) the parent, so the read document lines up
// with the desired document.
func (r *Resource) toProperties(raw props, parent string) props {
	out := make(props, len(r.def.Fields)+2)
	for _, field := range r.def.Fields {
		if v, ok := raw[r.def.apiName(field)]; ok {
			out[field] = v
		}
	}
	if id, ok := raw[r.def.idField()]; ok {
		out["id"] = id
	}
	if r.def.Scope != ScopeAccount && parent != "" {
		out[r.def.ParentProperty] = parent
	}
	return out
}

func (r *Resource) idOf(raw props) string {
	return stringField(raw, r.def.idField())
}

func stringField(raw props, field string) string {
	v, ok := raw[field]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (r *Resource) request(method, p string, body any) vercelapi.Request {
	return vercelapi.Request{Method: method, Path: p, Body: body, Query: r.def.Query}
}

// =============================================================================
// CRUD
// =============================================================================

func (r *Resource) Create(ctx context.Context, req *resource.CreateRequest) (*resource.CreateResult, error) {
	var desired props
	if err := json.Unmarshal(req.Properties, &desired); err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	parent := ""
	if r.def.Scope != ScopeAccount {
		v, _ := desired[r.def.ParentProperty].(string)
		if v == "" {
			return prov.FailCreate(resource.OperationErrorCodeInvalidRequest,
				fmt.Sprintf("%s is required", r.def.ParentProperty)), nil
		}
		parent = v
	}

	var created props
	err := r.client.Do(ctx,
		r.request(r.def.createMethod(), path(r.def.CollectionPath, parent, ""), r.body(desired, false)),
		&created)
	if err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	id := stringField(created, r.def.createIDField())
	if id == "" {
		return prov.FailCreate(resource.OperationErrorCodeServiceInternalError,
			fmt.Sprintf("create response missing %q", r.def.createIDField())), nil
	}
	return prov.SuccessCreate(r.joinNativeID(parent, id), r.toProperties(created, parent)), nil
}

func (r *Resource) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	parent, id, err := r.splitNativeID(req.NativeID)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeInvalidRequest}, nil
	}

	if r.def.ReadViaCollection || r.def.ItemPath == "" {
		return r.readViaCollection(ctx, req, parent, id)
	}

	var got props
	if err := r.client.Do(ctx, r.request("GET", path(r.def.ItemPath, parent, id), nil), &got); err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(r.toProperties(got, parent))),
	}, nil
}

// readViaCollection covers APIs with no usable single-item GET: list the
// collection and pick the entry out by id.
func (r *Resource) readViaCollection(ctx context.Context, req *resource.ReadRequest, parent, id string) (*resource.ReadResult, error) {
	items, err := r.collection(ctx, parent)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	for _, item := range items {
		if r.idOf(item) == id {
			return &resource.ReadResult{
				ResourceType: req.ResourceType,
				Properties:   string(prov.MustMarshal(r.toProperties(item, parent))),
			}, nil
		}
	}
	return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
}

func (r *Resource) Update(ctx context.Context, req *resource.UpdateRequest) (*resource.UpdateResult, error) {
	if r.def.NoUpdate {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest,
			fmt.Sprintf("%s cannot be updated in place", r.def.Type)), nil
	}
	parent, id, err := r.splitNativeID(req.NativeID)
	if err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	var desired props
	if err := json.Unmarshal(req.DesiredProperties, &desired); err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	var updated props
	err = r.client.Do(ctx,
		r.request(r.def.updateMethod(), path(r.def.itemPathFor("update"), parent, id), r.body(desired, true)),
		&updated)
	if err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessUpdate(req.NativeID, r.toProperties(updated, parent)), nil
}

func (r *Resource) Delete(ctx context.Context, req *resource.DeleteRequest) (*resource.DeleteResult, error) {
	if r.def.NoDelete {
		return prov.FailDelete(resource.OperationErrorCodeInvalidRequest,
			fmt.Sprintf("%s cannot be deleted through the API", r.def.Type)), nil
	}
	parent, id, err := r.splitNativeID(req.NativeID)
	if err != nil {
		return prov.FailDelete(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if err := r.client.Do(ctx, r.request("DELETE", path(r.def.itemPathFor("delete"), parent, id), nil), nil); err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}

func (r *Resource) Status(_ context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	// Every resource driven by this engine is synchronous.
	return prov.SuccessStatus(req.NativeID), nil
}

func (r *Resource) List(ctx context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	parents, err := r.parents(ctx)
	if err != nil {
		return &resource.ListResult{NativeIDs: []string{}}, nil
	}

	nativeIDs := []string{}
	for _, parent := range parents {
		items, err := r.collection(ctx, parent)
		if err != nil {
			// One unreadable parent must not abort discovery of the rest.
			continue
		}
		for _, item := range items {
			if id := r.idOf(item); id != "" {
				nativeIDs = append(nativeIDs, r.joinNativeID(parent, id))
			}
		}
	}
	return &resource.ListResult{NativeIDs: nativeIDs}, nil
}

// parents returns the ids List should enumerate under. Account-scoped
// resources get a single empty parent, meaning "one pass, no substitution".
func (r *Resource) parents(ctx context.Context) ([]string, error) {
	switch r.def.Scope {
	case ScopeProject:
		return prov.ProjectIDs(ctx, r.client, r.projectScope)
	case ScopeParent:
		return r.parentCollection(ctx)
	default:
		return []string{""}, nil
	}
}

func (r *Resource) parentCollection(ctx context.Context) ([]string, error) {
	if r.def.ParentListPath == "" {
		return nil, fmt.Errorf("%s: no ParentListPath, cannot enumerate parents", r.def.Type)
	}
	items, err := fetchList(ctx, r.client, r.def.ParentListPath, r.def.ParentListField, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		// Parent collections are keyed by "id" regardless of how the child
		// resource is keyed.
		if v, ok := item["id"].(string); ok && v != "" {
			ids = append(ids, v)
		}
	}
	return ids, nil
}

func (r *Resource) collection(ctx context.Context, parent string) ([]props, error) {
	return fetchList(ctx, r.client, path(r.def.CollectionPath, parent, ""), r.def.ListField, r.def.Query)
}

// fetchList GETs a collection and normalises the two shapes Vercel uses: a
// bare array, or an object with the array under a named key.
func fetchList(ctx context.Context, c *vercelapi.Client, p, field string, query map[string]string) ([]props, error) {
	var raw json.RawMessage
	if err := c.Do(ctx, vercelapi.Request{Method: "GET", Path: p, Query: query}, &raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []props
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, err
	}
	inner, ok := envelope[field]
	if !ok {
		// Several Vercel collection endpoints are documented only as "an
		// object"; fall back to the sole array-valued key rather than making
		// the caller guess it. Ambiguity is an error, not a coin flip.
		var found string
		for k, v := range envelope {
			if len(bytes.TrimSpace(v)) > 0 && bytes.TrimSpace(v)[0] == '[' {
				if found != "" {
					return nil, fmt.Errorf("%s: several array fields (%s, %s), set ListField", p, found, k)
				}
				found = k
			}
		}
		if found == "" {
			return nil, nil
		}
		inner = envelope[found]
	}
	var items []props
	if err := json.Unmarshal(inner, &items); err != nil {
		return nil, err
	}
	return items, nil
}
