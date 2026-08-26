package service

import (
	"embed"
	"time"

	"sigs.k8s.io/yaml"
)

// specFile is the OpenAPI document, compiled into the binary so a deployed
// instance can serve its own description and the two cannot get separated.
//
//go:embed openapi.yaml
var specFile embed.FS

// OpenAPI is this service's description, as written.
var OpenAPI = func() string {
	raw, err := specFile.ReadFile("openapi.yaml")
	if err != nil {
		// Unreachable: the file is embedded at build time, so a missing one is a
		// build failure rather than a runtime one.
		panic(err)
	}
	return string(raw)
}()

// OpenAPIJSON is the same document as JSON.
//
// The platform's API documentation application fetches a microservice's spec
// from the path named by openApiSpec in the manifest, and the convention there
// is JSON - hence two views of one document. Converted rather than kept twice,
// because two hand-written copies of the same thing are two copies that
// disagree.
//
// The error is kept rather than panicked on: a container that crashloops because
// a document would not convert is worse than one that serves everything except
// its own description. A test makes sure it converts.
var OpenAPIJSON, OpenAPIJSONError = func() ([]byte, error) {
	return yaml.YAMLToJSON([]byte(OpenAPI))
}()

// buildTime is what the served documents are dated. Zero rather than a real
// build stamp: it makes ServeContent skip Last-Modified and conditional
// requests, which is what we want for documents that only change with the
// binary.
var buildTime = time.Time{}
