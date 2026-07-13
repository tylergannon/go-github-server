module github.com/tylergannon/go-github-server

go 1.26

tool (
	golang.org/x/tools/cmd/goimports
	golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize
)

require (
	github.com/google/go-github/v89 v89.0.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/tools v0.47.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/kr/pretty v0.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/telemetry v0.0.0-20260625142307-59b4966ccb57 // indirect
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
