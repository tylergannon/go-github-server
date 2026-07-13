package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratorClassifiesEveryAnnotatedOperation(t *testing.T) {
	pkg, err := loadPackage()
	require.NoError(t, err)
	services, err := scan(pkg)
	require.NoError(t, err)
	_, coverage, err := render(services)
	require.NoError(t, err)

	assert.Len(t, services, 43)
	assert.Len(t, coverage, 1262)
	for _, operation := range coverage {
		assert.Contains(t, []string{"generated-clean", "generated-with-override", "generated-alias"}, operation.Status)
		assert.NotEmpty(t, operation.Source)
	}
	counts := map[string]int{}
	for _, operation := range coverage {
		counts[operation.HTTP+" "+operation.Path]++
	}
	for _, operation := range coverage {
		if counts[operation.HTTP+" "+operation.Path] > 1 {
			if operation.Status == "generated-alias" {
				assert.Contains(t, operation.Reasons, "canonical shared route used")
			} else {
				assert.Equal(t, "generated-with-override", operation.Status)
				assert.Contains(t, operation.Reasons, "shared HTTP operation")
			}
		}
	}
}

func TestServeMuxPatternsCanonicalizeWildcardNames(t *testing.T) {
	assert.Equal(t, "/repos/{p0}/{p1}/hooks/{p2}", serveMuxPath("/repos/{owner}/{repo}/hooks/{hook_id}"))
	assert.Equal(t, "/repos/{p0}/{p1}/contents/{p2...}", serveMuxPath("/repos/{owner}/{repo}/contents/{path}"))
}

func TestGeneratorExtractsAcceptDiscriminator(t *testing.T) {
	pkg, err := loadPackage()
	require.NoError(t, err)
	services, err := scan(pkg)
	require.NoError(t, err)
	for _, service := range services {
		if service.Name != "Repositories" {
			continue
		}
		for _, method := range service.Methods {
			if method.Name == "DownloadReleaseAsset" {
				assert.Contains(t, method.Accept, "application/octet-stream")
				assert.Equal(t, "download", method.ResponseKind)
				return
			}
		}
	}
	t.Fatal("DownloadReleaseAsset was not discovered")
}

func TestURLResultsAreGeneratedAsRedirects(t *testing.T) {
	pkg, err := loadPackage()
	require.NoError(t, err)
	services, err := scan(pkg)
	require.NoError(t, err)
	count := 0
	for _, service := range services {
		for _, method := range service.Methods {
			if isURLSignature(method.Signature) {
				count++
				assert.Equalf(t, "url", method.ResponseKind, "%s.%s", service.Name, method.Name)
			}
		}
	}
	assert.Equal(t, 5, count)
}

func TestResponseKindsConformToResultSignatures(t *testing.T) {
	pkg, err := loadPackage()
	require.NoError(t, err)
	services, err := scan(pkg)
	require.NoError(t, err)
	for _, service := range services {
		for _, method := range service.Methods {
			switch method.ResponseKind {
			case "bool":
				assert.Truef(t, firstResultIsBool(method.Signature), "%s.%s", service.Name, method.Name)
			case "raw":
				assert.Truef(t, firstResultIsRaw(method.Signature), "%s.%s", service.Name, method.Name)
			case "url":
				assert.Truef(t, isURLSignature(method.Signature) || firstResultIsString(method.Signature), "%s.%s", service.Name, method.Name)
			case "stream":
				assert.Truef(t, isStreamSignature(method.Signature), "%s.%s", service.Name, method.Name)
			case "download":
				assert.Truef(t, isDownloadSignature(method.Signature), "%s.%s", service.Name, method.Name)
			case "json":
			default:
				t.Fatalf("%s.%s has unknown response kind %q", service.Name, method.Name, method.ResponseKind)
			}
		}
	}
}
