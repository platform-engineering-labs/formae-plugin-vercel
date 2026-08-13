// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package prov

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
)

// listPageSize is what we ask Vercel for per page during discovery.
const listPageSize = 100

// projectListResponse covers both documented shapes of GET /v10/projects:
// a bare array, or an object with a pagination cursor.
type projectListResponse struct {
	Projects []struct {
		ID string `json:"id"`
	} `json:"projects"`
	Pagination *struct {
		Next json.Number `json:"next"`
	} `json:"pagination"`
}

func decodeProjectList(raw json.RawMessage) (projectListResponse, error) {
	var out projectListResponse
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return out, json.Unmarshal(trimmed, &out.Projects)
	}
	return out, json.Unmarshal(trimmed, &out)
}

// ProjectIDs returns the project ids discovery should iterate over. When
// `scoped` is non-empty only that one is returned — matching the `projectId`
// field in the target config, which keeps discovery of project sub-resources
// inside the conformance harness's timeout on large accounts. Otherwise every
// page of GET /v10/projects is walked.
func ProjectIDs(ctx context.Context, c *vercelapi.Client, scoped string) ([]string, error) {
	if scoped != "" {
		return []string{scoped}, nil
	}
	var ids []string
	from := ""
	for {
		query := map[string]string{"limit": strconv.Itoa(listPageSize)}
		if from != "" {
			query["from"] = from
		}
		var raw json.RawMessage
		if err := c.Do(ctx, vercelapi.Request{
			Method: "GET",
			Path:   "/v10/projects",
			Query:  query,
		}, &raw); err != nil {
			return ids, err
		}
		page, err := decodeProjectList(raw)
		if err != nil {
			return ids, err
		}
		for _, prj := range page.Projects {
			if prj.ID != "" {
				ids = append(ids, prj.ID)
			}
		}
		if page.Pagination == nil || page.Pagination.Next.String() == "" {
			return ids, nil
		}
		next := page.Pagination.Next.String()
		if next == from {
			// Defensive: a server that keeps returning the same cursor would
			// otherwise spin forever.
			return ids, nil
		}
		from = next
	}
}
