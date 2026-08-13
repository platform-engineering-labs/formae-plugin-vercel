// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package registry holds the per-resource-type Provisioner factory map.
//
// Each resource file in pkg/resources/* registers itself via init() at package
// import time. The main plugin looks up the factory by resource type and
// constructs a Provisioner bound to the live API client + target config.
package registry

import (
	"fmt"
	"sync"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/prov"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// TargetConfig is the deployment-level config carried in the forma target.
// Kept here (rather than in package main) so resource packages can depend on it
// without an import cycle.
//
// Field names are the PKL schema's rendered output names.
type TargetConfig struct {
	// BaseURL overrides https://api.vercel.com.
	BaseURL string `json:"BaseUrl"`
	// TeamID scopes every request to a Vercel team (?teamId=).
	TeamID string `json:"TeamId"`
	// Slug is the alternative team scope (?slug=). Ignored when TeamID is set.
	Slug string `json:"Slug"`
	// ProjectID optionally scopes sub-resource List()/discovery to a single
	// project. Without it, List() walks every project the token can see.
	ProjectID string `json:"ProjectId"`
}

// Factory builds a Provisioner bound to a live client and target.
type Factory func(client *vercelapi.Client, cfg *TargetConfig) prov.Provisioner

type registration struct {
	factory    Factory
	operations []resource.Operation
}

var (
	mu            sync.RWMutex
	registrations = make(map[string]*registration)
)

// Register associates a Vercel resource type with its supported operations and
// a Factory. Called from init() at package load.
func Register(resourceType string, operations []resource.Operation, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registrations[resourceType]; exists {
		panic(fmt.Sprintf("duplicate registration for %q", resourceType))
	}
	registrations[resourceType] = &registration{factory: factory, operations: operations}
}

// GetFactory returns the factory for a resource type.
func GetFactory(resourceType string) (Factory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registrations[resourceType]
	if !ok {
		return nil, false
	}
	return r.factory, true
}

// GetOperations returns the registered operations for a resource type.
func GetOperations(resourceType string) []resource.Operation {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := registrations[resourceType]
	if !ok {
		return nil
	}
	return r.operations
}

// ResourceTypes returns every registered type. Useful for plugin metadata.
func ResourceTypes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registrations))
	for t := range registrations {
		out = append(out, t)
	}
	return out
}

// Has reports whether a type is registered.
func Has(resourceType string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registrations[resourceType]
	return ok
}
