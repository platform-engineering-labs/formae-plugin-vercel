// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package rest

import (
	"context"
	"fmt"
	"sort"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// BagSpec declares a resource that is a whole keyed set rather than one item.
//
// Global Config items have no per-item endpoint: the entire set is written by
// one batch PATCH /v1/global-config/{id}/items. Modelling each entry as its own
// formae resource would make every concurrent apply a read-modify-write on
// shared state, and the last writer would win. The bag is therefore one
// resource holding a key -> value object, and every change is a single atomic
// call — the same shape as SUPABASE::Functions::Secrets.
//
// The bag has no id of its own, so its native id is the parent id.
type BagSpec struct {
	// Property is the declared field holding the whole set, as a JSON object of
	// key -> value. Values may be of any JSON type. It must also appear in
	// Fields.
	Property string

	// ItemsField is the request-body key holding the entry array. Default
	// "items".
	ItemsField string

	// KeyField and ValueField name each entry's key and value members, in both
	// the written batch and the read response. Defaults "key" and "value".
	KeyField   string
	ValueField string

	// OperationField names the per-entry verb, and UpsertOperation /
	// DeleteOperation are the values written into it. Defaults "operation",
	// "upsert" and "delete". Carrying the verb per entry is what lets one call
	// both add and remove keys.
	OperationField  string
	UpsertOperation string
	DeleteOperation string

	// WriteMethod defaults to PATCH and WritePath to CollectionPath.
	WriteMethod string
	WritePath   string

	// ReadPath defaults to CollectionPath. The response is the entry array,
	// named by the definition's ListField.
	ReadPath string
}

func (b *BagSpec) itemsField() string {
	if b.ItemsField != "" {
		return b.ItemsField
	}
	return "items"
}

func (b *BagSpec) keyField() string {
	if b.KeyField != "" {
		return b.KeyField
	}
	return "key"
}

func (b *BagSpec) valueField() string {
	if b.ValueField != "" {
		return b.ValueField
	}
	return "value"
}

func (b *BagSpec) operationField() string {
	if b.OperationField != "" {
		return b.OperationField
	}
	return "operation"
}

func (b *BagSpec) upsertOperation() string {
	if b.UpsertOperation != "" {
		return b.UpsertOperation
	}
	return "upsert"
}

func (b *BagSpec) deleteOperation() string {
	if b.DeleteOperation != "" {
		return b.DeleteOperation
	}
	return "delete"
}

func (b *BagSpec) writeMethod() string {
	if b.WriteMethod != "" {
		return b.WriteMethod
	}
	return "PATCH"
}

func (d Definition) bagWritePath() string {
	if d.Bag.WritePath != "" {
		return d.Bag.WritePath
	}
	return d.CollectionPath
}

func (d Definition) bagReadPath() string {
	if d.Bag.ReadPath != "" {
		return d.Bag.ReadPath
	}
	return d.CollectionPath
}

// values pulls the declared set out of a property document.
func (b *BagSpec) values(p props) (map[string]any, error) {
	raw, ok := p[b.Property]
	if !ok || raw == nil {
		return nil, fmt.Errorf("%s is required", b.Property)
	}
	set, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object of key -> value", b.Property)
	}
	return set, nil
}

// entry builds one batch entry. A delete carries no value: some APIs reject it.
func (b *BagSpec) entry(operation, key string, value any, withValue bool) map[string]any {
	e := map[string]any{
		b.operationField(): operation,
		b.keyField():       key,
	}
	if withValue {
		e[b.valueField()] = value
	}
	return e
}

// sortedKeys keeps the request body stable across applies, so an unchanged set
// produces an identical request.
func sortedKeys(set map[string]any) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bagWrite issues the one batch call. Nothing to write is not a call.
func (r *Resource) bagWrite(ctx context.Context, parent string, entries []map[string]any) error {
	if len(entries) == 0 {
		return nil
	}
	spec := r.def.Bag
	body := map[string]any{spec.itemsField(): entries}
	return r.client.Do(ctx,
		r.request(spec.writeMethod(), path(r.def.bagWritePath(), parent, ""), body),
		nil)
}

// bagCurrent reads the set the API currently holds.
func (r *Resource) bagCurrent(ctx context.Context, parent string) (map[string]any, error) {
	spec := r.def.Bag
	items, err := getPaged(ctx, r.client, path(r.def.bagReadPath(), parent, ""), r.def.ListField, r.def.PageParam, r.def.Query)
	if err != nil {
		return nil, err
	}
	set := make(map[string]any, len(items))
	for _, item := range items {
		key := stringField(item, spec.keyField())
		if key == "" {
			continue
		}
		set[key] = item[spec.valueField()]
	}
	return set, nil
}

// bagProperties is the property document for a bag: its parent and its set.
func (r *Resource) bagProperties(parent string, set map[string]any) props {
	return props{
		r.def.ParentProperty: parent,
		r.def.Bag.Property:   set,
	}
}

func (r *Resource) bagCreate(ctx context.Context, parent string, desired props) (*resource.CreateResult, error) {
	spec := r.def.Bag
	set, err := spec.values(desired)
	if err != nil {
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	if len(set) == 0 {
		// An empty bag is indistinguishable from an absent one on read, so
		// creating one would never converge.
		return prov.FailCreate(resource.OperationErrorCodeInvalidRequest,
			fmt.Sprintf("%s must hold at least one entry", spec.Property)), nil
	}
	entries := make([]map[string]any, 0, len(set))
	for _, key := range sortedKeys(set) {
		entries = append(entries, spec.entry(spec.upsertOperation(), key, set[key], true))
	}
	if err := r.bagWrite(ctx, parent, entries); err != nil {
		return prov.FailCreate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessCreate(parent, r.bagProperties(parent, set)), nil
}

func (r *Resource) bagRead(ctx context.Context, req *resource.ReadRequest, parent string) (*resource.ReadResult, error) {
	set, err := r.bagCurrent(ctx, parent)
	if err != nil {
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: vercelapi.ClassifyError(err)}, nil
	}
	if len(set) == 0 {
		// The set is the resource: with no entries there is nothing to manage.
		return &resource.ReadResult{ResourceType: req.ResourceType, ErrorCode: resource.OperationErrorCodeNotFound}, nil
	}
	return &resource.ReadResult{
		ResourceType: req.ResourceType,
		Properties:   string(prov.MustMarshal(r.bagProperties(parent, set))),
	}, nil
}

// bagUpdate reconciles the set in one call: every desired key is upserted and
// every key the API holds but the desired document does not is deleted.
// Removals are computed against live state rather than the prior document, so
// a key added out of band is cleaned up too.
func (r *Resource) bagUpdate(ctx context.Context, req *resource.UpdateRequest, parent string, desired props) (*resource.UpdateResult, error) {
	spec := r.def.Bag
	set, err := spec.values(desired)
	if err != nil {
		return prov.FailUpdate(resource.OperationErrorCodeInvalidRequest, err.Error()), nil
	}
	current, err := r.bagCurrent(ctx, parent)
	if err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}

	entries := make([]map[string]any, 0, len(set)+len(current))
	for _, key := range sortedKeys(set) {
		entries = append(entries, spec.entry(spec.upsertOperation(), key, set[key], true))
	}
	for _, key := range sortedKeys(current) {
		if _, keep := set[key]; !keep {
			entries = append(entries, spec.entry(spec.deleteOperation(), key, nil, false))
		}
	}
	if err := r.bagWrite(ctx, parent, entries); err != nil {
		return prov.FailUpdate(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessUpdate(req.NativeID, r.bagProperties(parent, set)), nil
}

// bagDelete removes every entry the bag holds. A parent that is already gone is
// success: the entries went with it.
func (r *Resource) bagDelete(ctx context.Context, req *resource.DeleteRequest, parent string) (*resource.DeleteResult, error) {
	spec := r.def.Bag
	current, err := r.bagCurrent(ctx, parent)
	if err != nil {
		if vercelapi.IsNotFound(err) {
			return prov.SuccessDelete(req.NativeID), nil
		}
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	entries := make([]map[string]any, 0, len(current))
	for _, key := range sortedKeys(current) {
		entries = append(entries, spec.entry(spec.deleteOperation(), key, nil, false))
	}
	if err := r.bagWrite(ctx, parent, entries); err != nil && !vercelapi.IsNotFound(err) {
		return prov.FailDelete(vercelapi.ClassifyError(err), err.Error()), nil
	}
	return prov.SuccessDelete(req.NativeID), nil
}
