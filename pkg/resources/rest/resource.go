// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// splitNativeID returns (parent, id). Account-scoped resources have no parent;
// singletons and bags have no id.
func (r *Resource) splitNativeID(nativeID string) (parent, id string, err error) {
	if err := r.def.requireParent(); err != nil {
		return "", "", err
	}
	if !r.def.hasOwnID() {
		if nativeID == "" {
			return "", "", fmt.Errorf("native id must be the %s", r.def.ParentProperty)
		}
		return nativeID, "", nil
	}
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
	if !r.def.hasOwnID() {
		return parent
	}
	if r.def.Scope == ScopeAccount {
		return id
	}
	return parent + "/" + id
}

// fillProps replaces "{prop:<field>}" placeholders with values from the
// desired properties. A missing or non-string value is a request error, not a
// silently malformed URL.
func fillProps(template string, p props) (string, error) {
	out := template
	for {
		start := strings.Index(out, "{prop:")
		if start < 0 {
			return out, nil
		}
		end := strings.Index(out[start:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated {prop: in path template %q", template)
		}
		field := out[start+len("{prop:") : start+end]
		v, _ := p[field].(string)
		if v == "" {
			return "", fmt.Errorf("%s is required", field)
		}
		out = out[:start] + v + out[start+end+1:]
	}
}

// path fills a template's {parent} and {id} placeholders.
func path(template, parent, id string) string {
	out := strings.ReplaceAll(template, "{parent}", parent)
	return strings.ReplaceAll(out, "{id}", id)
}

// fillTemplate substitutes {parent} and {id} through a request-body template,
// descending into nested objects and arrays. Values that are not strings pass
// through untouched.
func fillTemplate(v any, parent, id string) any {
	switch t := v.(type) {
	case string:
		return path(t, parent, id)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = fillTemplate(val, parent, id)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = fillTemplate(val, parent, id)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = path(val, parent, id)
		}
		return out
	default:
		return v
	}
}

// body builds a request payload from the declared fields. The parent property
// is excluded: it is a path segment, not a body field.
//
// When the definition declares a Wrap key the fields are nested under it,
// except those listed in WrapExclude, which stay beside the wrapper. Only the
// request is reshaped — responses are handled by Unwrap, independently.
func (r *Resource) body(p props, forUpdate bool) map[string]any {
	out := make(map[string]any, len(r.def.Fields))
	wrapped := map[string]any{}

	for _, field := range r.def.Fields {
		if forUpdate && r.def.isCreateOnly(field) {
			continue
		}
		v, ok := p[field]
		if !ok {
			continue
		}
		if r.def.Wrap != "" && !r.def.isWrapExcluded(field) {
			wrapped[r.def.apiName(field)] = v
			continue
		}
		out[r.def.apiName(field)] = v
	}

	if len(wrapped) > 0 {
		out[r.def.Wrap] = wrapped
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
	// A singleton or bag has no id of its own; reporting one would drift.
	if r.def.hasOwnID() {
		if id, ok := raw[r.def.idField()]; ok {
			out["id"] = id
		}
	}
	if r.def.Scope != ScopeAccount && parent != "" {
		out[r.def.ParentProperty] = parent
	}
	return out
}

// unwrap descends into the response envelope key when the definition declares
// one, e.g. {"repository": {...}} -> {...}.
func (r *Resource) unwrap(raw props) props {
	if r.def.Unwrap == "" {
		return raw
	}
	inner, ok := raw[r.def.Unwrap].(map[string]any)
	if !ok {
		return raw
	}
	return props(inner)
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

	if err := r.def.requireParent(); err != nil {
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

	if r.def.Bag != nil {
		// The whole set is written by one batch call; there is no item to POST.
		return r.bagCreate(ctx, parent, desired)
	}

	// An id taken from the desired document is checked before the write: an
	// association we cannot address afterwards is worse than one never made.
	declaredID := ""
	if r.def.CreateIDFromProperty != "" {
		declaredID, _ = desired[r.def.CreateIDFromProperty].(string)
		if declaredID == "" {
			return prov.FailCreate(resource.OperationErrorCodeInvalidRequest,
				fmt.Sprintf("%s is required", r.def.CreateIDFromProperty)), nil
		}
	}

	createPath, err := fillProps(path(r.def.createPath(), parent, ""), desired)
	if err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}

	var created props
	err = r.client.Do(ctx,
		r.request(r.def.createMethod(), createPath, r.body(desired, false)),
		&created)
	if err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	created = r.unwrap(created)

	// A singleton has no id to look for: the parent is the whole native id.
	if !r.def.hasOwnID() {
		if r.def.Async != nil {
			return r.asyncCreateResult(parent, created, parent), nil
		}
		return prov.SuccessCreate(parent, r.toProperties(created, parent)), nil
	}

	id := declaredID
	if id == "" {
		id = stringField(created, r.def.createIDField())
	}
	if id == "" {
		return prov.FailCreate(resource.OperationErrorCodeServiceInternalError,
			fmt.Sprintf("create response missing %q", r.def.createIDField())), nil
	}
	nativeID := r.joinNativeID(parent, id)
	if r.def.Async != nil {
		// The API accepted the request; that is not the same as the resource
		// existing. Let Status() decide.
		return r.asyncCreateResult(nativeID, created, parent), nil
	}
	return prov.SuccessCreate(nativeID, r.toProperties(created, parent)), nil
}

func (r *Resource) Read(ctx context.Context, req *resource.ReadRequest) (*resource.ReadResult, error) {
	parent, id, err := r.splitNativeID(req.NativeID)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeInvalidRequest}, nil
	}
	if r.def.Bag != nil {
		return r.bagRead(ctx, req, parent)
	}
	raw, err := r.fetch(ctx, parent, id)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: classifyFetchError(err)}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(r.toProperties(raw, parent))),
	}, nil
}

// errNoSuchItem is returned by fetch when a collection scan finds no entry with
// the wanted id — the collection-scan equivalent of a 404.
var errNoSuchItem = errors.New("no such item in collection")

// fetch returns the raw API object for one resource: unwrapped, but not yet
// narrowed to the declared fields. Read narrows it; Status reads the async
// lifecycle field out of it, which is deliberately never a declared field.
func (r *Resource) fetch(ctx context.Context, parent, id string) (props, error) {
	// A singleton lives at a fixed path under its parent; there is nothing to
	// scan for and no id to match on.
	if !r.def.hasOwnID() {
		var got props
		if err := r.client.Do(ctx, r.request("GET", path(r.def.singletonPath(), parent, ""), nil), &got); err != nil {
			return nil, err
		}
		return r.unwrap(got), nil
	}

	// No usable single-item GET: list the collection and pick the entry out.
	if r.def.ReadViaCollection || r.def.ItemPath == "" {
		items, err := r.readCollection(ctx, parent)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if r.idOf(item) == id {
				return item, nil
			}
		}
		return nil, errNoSuchItem
	}

	var got props
	if err := r.client.Do(ctx, r.request("GET", path(r.def.ItemPath, parent, id), nil), &got); err != nil {
		return nil, err
	}
	return r.unwrap(got), nil
}

// classifyFetchError maps a fetch failure to a formae error code, including the
// collection-scan miss that never reaches the transport layer.
func classifyFetchError(err error) resource.OperationErrorCode {
	if errors.Is(err, errNoSuchItem) {
		return resource.OperationErrorCodeNotFound
	}
	return vercelapi.ClassifyError(err)
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

	if r.def.Bag != nil {
		return r.bagUpdate(ctx, req, parent, desired)
	}

	var updated props
	err = r.client.Do(ctx,
		r.request(r.def.updateMethod(), path(r.def.itemPathFor("update"), parent, id), r.body(desired, true)),
		&updated)
	if err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	updated = r.unwrap(updated)
	if r.def.Async != nil {
		return r.asyncUpdateResult(req.NativeID, updated, parent), nil
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
	if r.def.Bag != nil {
		return r.bagDelete(ctx, req, parent)
	}
	// Some deletes name what to remove in a body rather than in the path, and
	// some are not even a DELETE — an unlink is usually a POST.
	var body any
	if r.def.DeleteBody != nil {
		body = fillTemplate(map[string]any(r.def.DeleteBody), parent, id)
	}
	if err := r.client.Do(ctx, r.request(r.def.deleteMethod(), path(r.def.itemPathFor("delete"), parent, id), body), nil); err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}

func (r *Resource) Status(ctx context.Context, req *resource.StatusRequest) (*resource.StatusResult, error) {
	if r.def.Async == nil {
		// Resources that do not declare an async lifecycle are synchronous:
		// the write already returned the final state.
		return prov.SuccessStatus(req.NativeID), nil
	}
	return r.asyncStatus(ctx, req), nil
}

func (r *Resource) List(ctx context.Context, _ *resource.ListRequest) (*resource.ListResult, error) {
	parents, err := r.parents(ctx)
	if err != nil {
		// Cannot even enumerate what to look under — that is a failure, not an
		// empty account. Swallowing it here would make a bad token look
		// identical to a account with nothing in it.
		return &resource.ListResult{NativeIDs: []string{}}, err
	}

	nativeIDs := []string{}

	// One singleton (or bag) per parent, always at the same path: enumerating
	// the parents is enumerating the resources. Whether each parent actually
	// has one is Read's job — it reports NotFound and the agent converges.
	if !r.def.hasOwnID() {
		for _, parent := range parents {
			if parent != "" {
				nativeIDs = append(nativeIDs, parent)
			}
		}
		return &resource.ListResult{NativeIDs: nativeIDs}, nil
	}

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
	if _, flat := r.def.listPath(); flat {
		// A flat list path enumerates everything in one pass; there is no
		// parent to substitute.
		return []string{""}, nil
	}
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
		if v, ok := item[r.def.parentIDField()].(string); ok && v != "" {
			ids = append(ids, v)
		}
	}
	return ids, nil
}

func (r *Resource) collection(ctx context.Context, parent string) ([]props, error) {
	p, _ := r.def.listPath()
	return fetchList(ctx, r.client, path(p, parent, ""), r.def.ListField, r.def.Query)
}

// readCollection is what Read scans. A parent-scoped ListPath is the right
// source when the API creates and lists at different paths — a Global Config
// token is POSTed to .../token but listed at .../tokens. A flat ListPath is not
// scoped to this resource's parent, so CollectionPath is scanned instead.
func (r *Resource) readCollection(ctx context.Context, parent string) ([]props, error) {
	p, flat := r.def.listPath()
	if flat {
		p = r.def.CollectionPath
	}
	return fetchList(ctx, r.client, path(p, parent, ""), r.def.ListField, r.def.Query)
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
