// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package main

import (
	"context"
	"testing"

	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// clearTokens removes both accepted token variables for the duration of a test.
func clearTokens(t *testing.T) {
	t.Helper()
	for _, name := range tokenEnvVars {
		t.Setenv(name, "")
	}
}

// A missing token must NOT look like an empty account.
//
// This is a regression guard: List used to swallow every dispatch error and
// answer with an empty set, so an agent running without VERCEL_TOKEN reported
// "discovered nothing" and gave no clue why. Discovery silently finding nothing
// is far worse than discovery failing loudly.
func TestList_MissingTokenIsAnError(t *testing.T) {
	clearTokens(t)
	p := &Plugin{}
	res, err := p.List(context.Background(), &resource.ListRequest{
		ResourceType: "VERCEL::Projects::Project",
	})
	if err == nil {
		t.Fatal("List with no token returned nil error — a misconfigured agent would look like an empty account")
	}
	if res == nil || len(res.NativeIDs) != 0 {
		t.Errorf("expected an empty result alongside the error, got %+v", res)
	}
}

// A type this plugin does not handle is genuinely nothing to report: the agent
// asks every plugin about every type it knows.
func TestList_UnknownResourceTypeIsQuiet(t *testing.T) {
	t.Setenv(tokenEnvVars[0], "tok")
	p := &Plugin{}
	res, err := p.List(context.Background(), &resource.ListRequest{
		ResourceType: "AWS::S3::Bucket",
	})
	if err != nil {
		t.Fatalf("unknown type should not be an error, got %v", err)
	}
	if len(res.NativeIDs) != 0 {
		t.Errorf("expected no ids, got %v", res.NativeIDs)
	}
}

// Credentials problems are reported through the result's ErrorCode on the CRUD
// paths, not as a returned error, so a bad token does not abort a reconcile.
func TestCreate_MissingTokenReportsInvalidCredentials(t *testing.T) {
	clearTokens(t)
	p := &Plugin{}
	res, err := p.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "VERCEL::Projects::Project",
		Properties:   []byte(`{"name":"x"}`),
	})
	if err != nil {
		t.Fatalf("credentials failures should surface via ErrorCode, not a returned error: %v", err)
	}
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidCredentials {
		t.Errorf("ErrorCode = %v, want InvalidCredentials", res.ProgressResult.ErrorCode)
	}
}

// An unsupported resource type must stop a reconcile rather than be retried.
func TestCreate_UnknownResourceTypeIsHardError(t *testing.T) {
	t.Setenv(tokenEnvVars[0], "tok")
	p := &Plugin{}
	_, err := p.Create(context.Background(), &resource.CreateRequest{
		ResourceType: "AWS::S3::Bucket",
		Properties:   []byte(`{}`),
	})
	if err == nil {
		t.Fatal("unknown resource type should return a hard error")
	}
}
