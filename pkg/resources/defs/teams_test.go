// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unit

package defs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/registry"
	"github.com/platform-engineering-labs/formae-plugin-vercel/pkg/resources/rest"
	vercelapi "github.com/platform-engineering-labs/formae-plugin-vercel/pkg/transport/vercel"
	"github.com/platform-engineering-labs/formae/pkg/plugin/resource"
)

// Obviously fake material. Nothing here is a key, and nothing in this package
// ever talks to api.vercel.com.
const (
	fakeCert = "-----BEGIN CERTIFICATE-----\nFAKE-LEAF-NOT-A-REAL-CERTIFICATE\n-----END CERTIFICATE-----"
	fakeCA   = "-----BEGIN CERTIFICATE-----\nFAKE-ROOT-NOT-A-REAL-CERTIFICATE\n-----END CERTIFICATE-----"
	fakeKey  = "-----BEGIN PRIVATE KEY-----\nFAKE-NOT-A-REAL-PRIVATE-KEY\n-----END PRIVATE KEY-----"
)

// uploadResponse is the documented 200 body of PUT /v8/certs and GET
// /v8/certs/{id}: id, createdAt, expiresAt, autoRenew, cns — and no PEM.
// https://vercel.com/docs/rest-api/certs/upload-a-cert
const uploadResponse = `{"id":"cert_1","createdAt":1700000000000,"expiresAt":1800000000000,
	"autoRenew":false,"cns":["example.com","www.example.com"]}`

func teamsClient(t *testing.T, h http.HandlerFunc) *vercelapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := vercelapi.NewClient(vercelapi.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func uploadedCertProvisioner(t *testing.T, h http.HandlerFunc) *rest.Resource {
	t.Helper()
	return rest.New(uploadedCertificate(), teamsClient(t, h), "")
}

func desiredUploadedCert(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"cert":           fakeCert,
		"ca":             fakeCA,
		"key":            fakeKey,
		"skipValidation": true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// =============================================================================
// VERCEL::Certs::UploadedCertificate — create
// =============================================================================

// The upload flow is a PUT, not a POST: POST /v8/certs asks Vercel to *issue* a
// certificate and is a different resource type entirely.
func TestUploadedCertificate_CreateIsAPutWithThePEMBlobs(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/v8/certs" {
			t.Errorf("path = %s, want /v8/certs", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		// Wire names come from the endpoint's own reference page: ca, key,
		// cert, skipValidation.
		if body["cert"] != fakeCert {
			t.Errorf("cert = %v", body["cert"])
		}
		if body["ca"] != fakeCA {
			t.Errorf("ca = %v", body["ca"])
		}
		if body["key"] != fakeKey {
			t.Errorf("key = %v", body["key"])
		}
		if body["skipValidation"] != true {
			t.Errorf("skipValidation = %v", body["skipValidation"])
		}
		// `cns` is derived by Vercel from the uploaded certificate; sending it
		// would be a request the API does not document.
		if _, leaked := body["cns"]; leaked {
			t.Error("cns must not be sent on the upload flow")
		}
		_, _ = io.WriteString(w, uploadResponse)
	})

	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: desiredUploadedCert(t)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Fatalf("status = %v: %s", res.ProgressResult.OperationStatus, res.ProgressResult.StatusMessage)
	}
	if res.ProgressResult.NativeID != "cert_1" {
		t.Errorf("NativeID = %q, want cert_1", res.ProgressResult.NativeID)
	}
}

// Whatever the upload answers with, the apply output must not carry the key
// material back out.
func TestUploadedCertificate_CreateResultOmitsSecrets(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, uploadResponse)
	})
	res, err := p.Create(context.Background(), &resource.CreateRequest{Properties: desiredUploadedCert(t)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertNoSecrets(t, res.ProgressResult.ResourceProperties)
}

// =============================================================================
// VERCEL::Certs::UploadedCertificate — read
// =============================================================================

func TestUploadedCertificate_ReadFiltersToDeclaredFields(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v8/certs/cert_1" {
			t.Errorf("got %s %s, want GET /v8/certs/cert_1", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, uploadResponse)
	})
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "cert_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %v", res.ErrorCode)
	}
	got := decodeProps(t, []byte(res.Properties))
	if got["id"] != "cert_1" {
		t.Errorf("id = %v", got["id"])
	}
	// Server-side bookkeeping is not managed state; surfacing it would report
	// drift on every sync.
	for _, unmanaged := range []string{"createdAt", "expiresAt", "autoRenew", "cns"} {
		if _, ok := got[unmanaged]; ok {
			t.Errorf("unmanaged field %q leaked into the read document", unmanaged)
		}
	}
}

// The point of the whole resource: the certificate, the CA chain and above all
// the private key are write-only. They go up once and never come back.
func TestUploadedCertificate_ReadNeverSurfacesSecrets(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, uploadResponse)
	})
	res, err := p.Read(context.Background(), &resource.ReadRequest{NativeID: "cert_1"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertNoSecrets(t, []byte(res.Properties))
}

// The same guarantee, restated where it is actually enforceable: the PEM fields
// are create-only and the resource has no update verb, so nothing after the
// initial PUT ever puts key material on the wire.
func TestUploadedCertificate_SecretsAreWriteOnceOnly(t *testing.T) {
	def := uploadedCertificate()
	if !def.NoUpdate {
		t.Error("the certs API has no PATCH; NoUpdate must be set")
	}
	for _, secret := range []string{"cert", "ca", "key"} {
		if !contains(def.Fields, secret) {
			t.Errorf("%q must be a declared field or the upload body is incomplete", secret)
		}
		if !contains(def.CreateOnly, secret) {
			t.Errorf("%q must be createOnly", secret)
		}
	}
}

func TestUploadedCertificate_ReadNotFound(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"nope"}}`)
	})
	res, _ := p.Read(context.Background(), &resource.ReadRequest{NativeID: "cert_gone"})
	if res.ErrorCode != resource.OperationErrorCodeNotFound {
		t.Errorf("ErrorCode = %v, want NotFound", res.ErrorCode)
	}
}

// =============================================================================
// VERCEL::Certs::UploadedCertificate — update, delete, list
// =============================================================================

func TestUploadedCertificate_UpdateIsRejected(t *testing.T) {
	p := rest.New(uploadedCertificate(), nil, "")
	res, err := p.Update(context.Background(), &resource.UpdateRequest{
		NativeID:          "cert_1",
		DesiredProperties: desiredUploadedCert(t),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ProgressResult.ErrorCode != resource.OperationErrorCodeInvalidRequest {
		t.Errorf("ErrorCode = %v, want InvalidRequest", res.ProgressResult.ErrorCode)
	}
}

// An out-of-band removal must converge, not fail the reconcile.
func TestUploadedCertificate_DeleteIsIdempotentOn404(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v8/certs/cert_1" {
			t.Errorf("got %s %s, want DELETE /v8/certs/cert_1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"gone"}}`)
	})
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "cert_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

func TestUploadedCertificate_DeleteSucceeds(t *testing.T) {
	// remove-cert documents its 200 body only as "an object"; the engine
	// discards it either way.
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})
	res, err := p.Delete(context.Background(), &resource.DeleteRequest{NativeID: "cert_1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.ProgressResult.OperationStatus != resource.OperationStatusSuccess {
		t.Errorf("status = %v, want Success", res.ProgressResult.OperationStatus)
	}
}

// GET /v8/certs answers {certs, pagination}; the envelope key has to be
// declared or the engine has to guess it.
func TestUploadedCertificate_List(t *testing.T) {
	p := uploadedCertProvisioner(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v8/certs" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"certs":[{"id":"cert_1"},{"id":"cert_2"}],
			"pagination":{"count":2,"next":null,"prev":null}}`)
	})
	res, err := p.List(context.Background(), &resource.ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.NativeIDs) != 2 || res.NativeIDs[0] != "cert_1" || res.NativeIDs[1] != "cert_2" {
		t.Errorf("NativeIDs = %v", res.NativeIDs)
	}
}

// =============================================================================
// Definition shape
// =============================================================================

func TestTeamsGroupIsWellFormed(t *testing.T) {
	group := teams()
	if len(group) == 0 {
		t.Fatal("teams() declared nothing")
	}
	for _, def := range group {
		if strings.Count(def.Type, "::") != 2 || !strings.HasPrefix(def.Type, "VERCEL::") {
			t.Errorf("%s: type must be VERCEL::Category::Resource", def.Type)
		}
		if def.CollectionPath == "" {
			t.Errorf("%s: no CollectionPath", def.Type)
		}
		if len(def.Fields) == 0 {
			t.Errorf("%s: no Fields", def.Type)
		}
		if def.ItemPath == "" && !def.ReadViaCollection {
			t.Errorf("%s: Read cannot work", def.Type)
		}
		if !def.NoUpdate && allCreateOnly(def) {
			t.Errorf("%s: every field is createOnly but NoUpdate is not set", def.Type)
		}
		for _, co := range def.CreateOnly {
			if !contains(def.Fields, co) {
				t.Errorf("%s: createOnly %q is not a declared field", def.Type, co)
			}
		}
		if !registry.Has(def.Type) {
			t.Errorf("%s: not registered", def.Type)
		}
	}
}

func TestUploadedCertificateDefinition(t *testing.T) {
	def := uploadedCertificate()
	if def.Type != "VERCEL::Certs::UploadedCertificate" {
		t.Errorf("Type = %q", def.Type)
	}
	if def.Scope != rest.ScopeAccount {
		t.Errorf("Scope = %v, want ScopeAccount — certs hang off the team, not a project", def.Scope)
	}
	if def.CreateMethod != http.MethodPut {
		t.Errorf("CreateMethod = %q, want PUT", def.CreateMethod)
	}
	if def.CollectionPath != "/v8/certs" || def.ItemPath != "/v8/certs/{id}" {
		t.Errorf("paths = %q, %q", def.CollectionPath, def.ItemPath)
	}
	if def.ListField != "certs" {
		t.Errorf("ListField = %q, want certs", def.ListField)
	}
	want := []string{"cert", "ca", "key", "skipValidation"}
	if len(def.Fields) != len(want) {
		t.Fatalf("Fields = %v, want %v", def.Fields, want)
	}
	for _, f := range want {
		if !contains(def.Fields, f) {
			t.Errorf("Fields missing %q", f)
		}
	}
}

// The issue flow and the upload flow are two resources over one collection.
// This guards the split: nothing here may quietly turn ::Certificate into the
// upload endpoint.
func TestIssuedAndUploadedCertificatesStaySeparate(t *testing.T) {
	var issued, uploaded *rest.Definition
	for _, def := range All() {
		switch def.Type {
		case "VERCEL::Certs::Certificate":
			issued = &def
		case "VERCEL::Certs::UploadedCertificate":
			uploaded = &def
		}
	}
	if issued == nil {
		t.Fatal("VERCEL::Certs::Certificate disappeared")
	}
	if uploaded == nil {
		t.Fatal("VERCEL::Certs::UploadedCertificate is not declared")
	}
	if issued.CreateMethod != "" {
		t.Errorf("the issue flow must stay a POST, got CreateMethod=%q", issued.CreateMethod)
	}
	if !contains(issued.Fields, "cns") {
		t.Error("the issue flow must keep its cns field")
	}
	for _, secret := range []string{"cert", "ca", "key"} {
		if contains(issued.Fields, secret) {
			t.Errorf("%q leaked into the issue flow", secret)
		}
	}
	if contains(uploaded.Fields, "cns") {
		t.Error("cns is derived from the uploaded certificate, not an input")
	}
}

// VERCEL::Teams::Member is deliberately absent. The team id is target-level
// configuration carried on the transport client, and rest.Definition's path
// templates can only interpolate the resource's own properties — so
// /v3/teams/{teamId}/members cannot be addressed without either an engine
// change or restating the target's team as a resource property. See the note in
// teams.go.
func TestTeamMemberIsNotDeclared(t *testing.T) {
	for _, def := range All() {
		if def.Type == "VERCEL::Teams::Member" {
			t.Fatal("VERCEL::Teams::Member is declared; the teamId path segment " +
				"cannot be filled from target config — see teams.go")
		}
	}
}

// =============================================================================
// Helpers
// =============================================================================

func decodeProps(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return got
}

// assertNoSecrets fails if any property name or value looks like key material.
// Checking values as well as names catches a leak that arrives under a name
// nobody predicted.
func assertNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	for _, name := range []string{"cert", "ca", "key", "privateKey", "certificate"} {
		if _, ok := decodeProps(t, raw)[name]; ok {
			t.Errorf("secret field %q surfaced in %s", name, raw)
		}
	}
	for _, marker := range []string{"BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "FAKE-"} {
		if strings.Contains(string(raw), marker) {
			t.Errorf("PEM material (%q) surfaced in %s", marker, raw)
		}
	}
}
