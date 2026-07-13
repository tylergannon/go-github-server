// Command gen-server derives server interfaces and HTTP route metadata from
// the annotated service methods in github.com/google/go-github.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

const githubPackage = "github.com/google/go-github/v89/github"

var operationPattern = regexp.MustCompile(`(?i)^//\s*meta:operation\s+(\S+)\s+(\S+)\s*$`)
var placeholderPattern = regexp.MustCompile(`\{([^}]+)\}`)

type route struct {
	Method string
	Path   string
}

type method struct {
	Service      string
	Name         string
	Signature    *types.Signature
	Decl         *ast.FuncDecl
	Routes       []route
	ParamNames   []string
	QueryParams  map[string]bool
	QueryValues  map[string]string
	BodyParams   map[string]bool
	BodyFields   map[string]string
	UploadParams map[string]bool
	ResponseKind string
	Accept       []string
	Direct       bool
	Source       string
	Override     []string
	PathBuilds   []pathBuild
	BindsAppJWT  bool
}

type pathBuild struct {
	skeleton string
	sources  []pathSource
}

type pathSource struct {
	index int
	field string
}

type service struct {
	Name    string
	Methods []*method
}

type importSet struct {
	byPath map[string]string
}

type coverageEntry struct {
	Service string   `json:"service"`
	Method  string   `json:"method"`
	HTTP    string   `json:"http_method"`
	Path    string   `json:"path"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
	Source  string   `json:"source"`
}

func main() {
	output := flag.String("output", "zz_generated.go", "generated Go output")
	coverageOutput := flag.String("coverage", "coverage.json", "operation coverage report")
	check := flag.Bool("check", false, "check that generated files are current")
	flag.Parse()

	pkg, err := loadPackage()
	if err != nil {
		fatal(err)
	}
	services, err := scan(pkg)
	if err != nil {
		fatal(err)
	}
	generated, coverage, err := render(services)
	if err != nil {
		fatal(err)
	}
	formatted, err := format.Source(generated)
	if err != nil {
		fatal(fmt.Errorf("format generated source: %w\n%s", err, generated))
	}
	coverageJSON, err := json.MarshalIndent(coverage, "", "  ")
	if err != nil {
		fatal(err)
	}
	coverageJSON = append(coverageJSON, '\n')

	if *check {
		checkFile(*output, formatted)
		checkFile(*coverageOutput, coverageJSON)
		return
	}
	if err := os.WriteFile(*output, formatted, 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*coverageOutput, coverageJSON, 0o644); err != nil {
		fatal(err)
	}
}

func loadPackage() (*packages.Package, error) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, githubPackage)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) != 0 || len(pkgs) != 1 {
		return nil, fmt.Errorf("load %s: expected one error-free package", githubPackage)
	}
	return pkgs[0], nil
}

func scan(pkg *packages.Package) ([]*service, error) {
	byName := map[string]*service{}
	for fileIndex, file := range pkg.Syntax {
		filename := pkg.CompiledGoFiles[fileIndex]
		if strings.HasSuffix(filename, "_test.go") || strings.HasPrefix(filepath.Base(filename), "gen-") {
			continue
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !function.Name.IsExported() {
				continue
			}
			routes := routesFromDoc(function.Doc)
			if len(routes) == 0 {
				continue
			}
			serviceName := receiverName(function)
			if !strings.HasSuffix(serviceName, "Service") {
				continue
			}
			object, ok := pkg.TypesInfo.Defs[function.Name].(*types.Func)
			if !ok {
				return nil, fmt.Errorf("%s: no type information for %s", filename, function.Name)
			}
			signature := object.Type().(*types.Signature)
			m := &method{
				Service:      strings.TrimSuffix(serviceName, "Service"),
				Name:         function.Name.Name,
				Signature:    signature,
				Decl:         function,
				Routes:       routes,
				ParamNames:   parameterNames(function),
				QueryParams:  map[string]bool{},
				QueryValues:  map[string]string{},
				BodyParams:   map[string]bool{},
				BodyFields:   map[string]string{},
				UploadParams: map[string]bool{},
				Source:       fmt.Sprintf("%s:%d", filepath.Base(filename), pkg.Fset.Position(function.Pos()).Line),
			}
			analyzeBody(m, pkg.TypesInfo)
			addServerParameters(m)
			if slices.ContainsFunc(routes, func(route route) bool { return route.Path == "/hub" }) && signature.Params().Len() != 6 {
				return nil, fmt.Errorf("%s: /hub adapter requires the six-parameter WebSub signature", m.Source)
			}
			if len(routes) > 1 {
				m.Override = append(m.Override, "multiple routes")
			}
			entry := byName[m.Service]
			if entry == nil {
				entry = &service{Name: m.Service}
				byName[m.Service] = entry
			}
			entry.Methods = append(entry.Methods, m)
		}
	}
	services := make([]*service, 0, len(byName))
	for _, entry := range byName {
		slices.SortFunc(entry.Methods, func(a, b *method) int { return strings.Compare(a.Name, b.Name) })
		services = append(services, entry)
	}
	slices.SortFunc(services, func(a, b *service) int { return strings.Compare(a.Name, b.Name) })
	return services, nil
}

func routesFromDoc(doc *ast.CommentGroup) []route {
	if doc == nil {
		return nil
	}
	var routes []route
	for _, comment := range doc.List {
		match := operationPattern.FindStringSubmatch(comment.Text)
		if len(match) == 3 {
			routes = append(routes, route{Method: strings.ToUpper(match[1]), Path: match[2]})
		}
	}
	return routes
}

func methodDocumentation(method *method) string {
	var sections []string
	if method.Decl.Doc != nil {
		if upstream := strings.TrimSpace(method.Decl.Doc.Text()); upstream != "" {
			sections = append(sections, upstream)
		}
	}
	if method.BindsAppJWT {
		sections = append(sections, "The appJWT parameter contains the credential from the required Authorization: Bearer <JWT> header.")
	}
	var routes []string
	for _, annotated := range method.Routes {
		effective := effectiveRoute(method, annotated)
		routes = append(routes, fmt.Sprintf("HTTP: %s %s", effective.Method, effective.Path))
	}
	if len(routes) > 0 {
		sections = append(sections, strings.Join(routes, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func addServerParameters(method *method) {
	if method.Service != "Apps" || (method.Name != "CreateInstallationToken" && method.Name != "CreateInstallationTokenListRepos") {
		return
	}
	parameters := method.Signature.Params()
	serverParameters := make([]*types.Var, 0, parameters.Len()+1)
	serverParameters = append(serverParameters, parameters.At(0))
	serverParameters = append(serverParameters, types.NewVar(token.NoPos, nil, "appJWT", types.Typ[types.String]))
	for index := 1; index < parameters.Len(); index++ {
		serverParameters = append(serverParameters, parameters.At(index))
	}
	method.Signature = types.NewSignatureType(
		nil,
		nil,
		nil,
		types.NewTuple(serverParameters...),
		method.Signature.Results(),
		method.Signature.Variadic(),
	)
	method.ParamNames = slices.Insert(method.ParamNames, 1, "appJWT")
	for buildIndex := range method.PathBuilds {
		for sourceIndex := range method.PathBuilds[buildIndex].sources {
			if method.PathBuilds[buildIndex].sources[sourceIndex].index > 0 {
				method.PathBuilds[buildIndex].sources[sourceIndex].index++
			}
		}
	}
	method.BindsAppJWT = true
	method.Override = appendUnique(method.Override, "authorization header argument")
}

func commentBlock(documentation string) string {
	var result strings.Builder
	for index, line := range strings.Split(documentation, "\n") {
		if index > 0 {
			result.WriteByte('\n')
		}
		result.WriteString("\t//")
		if line != "" {
			result.WriteByte(' ')
			result.WriteString(line)
		}
	}
	return result.String()
}

func effectiveRoute(method *method, annotated route) route {
	effective := annotated
	if method.Name == "GetLatestCodespaceExport" {
		effective.Path = strings.Replace(effective.Path, "{export_id}", "latest", 1)
	}
	if method.Name == "DownloadArtifact" {
		effective.Path = strings.Replace(effective.Path, "{archive_format}", "zip", 1)
	}
	return effective
}

func receiverName(function *ast.FuncDecl) string {
	expression := function.Recv.List[0].Type
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	if ident, ok := expression.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func parameterNames(function *ast.FuncDecl) []string {
	var names []string
	for _, field := range function.Type.Params.List {
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

func analyzeBody(m *method, info *types.Info) {
	definitions := map[string]ast.Expr{}
	ast.Inspect(m.Decl.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for i, left := range value.Lhs {
				if i >= len(value.Rhs) {
					continue
				}
				if ident, ok := left.(*ast.Ident); ok {
					definitions[ident.Name] = value.Rhs[i]
				}
			}
		case *ast.ValueSpec:
			for i, name := range value.Names {
				if i < len(value.Values) {
					definitions[name.Name] = value.Values[i]
				}
			}
		}
		return true
	})
	params := make(map[string]bool, len(m.ParamNames))
	for _, name := range m.ParamNames {
		params[name] = true
	}
	ast.Inspect(m.Decl.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledName(call.Fun)
		if name == "Sprintf" && len(call.Args) > 1 {
			analyzePathBuild(m, call, definitions, params, info)
			analyzeFormattedQuery(m, call, definitions, params, info)
		}
		switch name {
		case "addOptions":
			if len(call.Args) > 1 {
				if ident, ok := call.Args[1].(*ast.Ident); ok && params[ident.Name] {
					m.QueryParams[ident.Name] = true
				}
			}
		case "NewRequest":
			m.Direct = true
			if len(call.Args) > 3 && !isNil(call.Args[3]) {
				for parameter := range dependentParams(call.Args[3], definitions, params, map[string]bool{}) {
					m.BodyParams[parameter] = true
				}
				fields := map[string][]string{}
				collectBodyFields(call.Args[3], definitions, params, info, fields, map[string]bool{})
				for parameter, names := range fields {
					slices.Sort(names)
					names = slices.Compact(names)
					if len(names) == 1 {
						m.BodyFields[parameter] = names[0]
					}
				}
			}
		case "NewUploadRequest":
			m.Direct = true
			m.Override = appendUnique(m.Override, "binary upload")
			for _, argument := range call.Args[2:] {
				for parameter := range dependentParams(argument, definitions, params, map[string]bool{}) {
					m.UploadParams[parameter] = true
				}
			}
		case "NewFormRequest":
			m.Direct = true
			m.Override = appendUnique(m.Override, "form body")
		case "parseBoolResponse":
			m.ResponseKind = "bool"
		case "bareDoUntilFound":
			m.Direct = true
			m.ResponseKind = "url"
		}
		if name == "Set" && len(call.Args) == 2 && stringConstant(info, call.Args[0]) == "Accept" {
			if mediaType := stringConstant(info, call.Args[1]); mediaType != "" {
				m.Accept = appendUnique(m.Accept, mediaType)
			}
		}
		return true
	})
	syntaxKind := m.ResponseKind
	m.ResponseKind = "json"
	if isDownloadSignature(m.Signature) {
		m.ResponseKind = "download"
	} else if isStreamSignature(m.Signature) {
		m.ResponseKind = "stream"
	} else if isURLSignature(m.Signature) {
		m.ResponseKind = "url"
	} else if firstResultIsBool(m.Signature) {
		m.ResponseKind = "bool"
	} else if firstResultIsRaw(m.Signature) && (syntaxKind == "raw" || containsBytesBuffer(m.Decl, info)) {
		m.ResponseKind = "raw"
	} else if firstResultIsString(m.Signature) && (syntaxKind == "url" || strings.HasSuffix(m.Name, "MigrationArchiveURL")) {
		m.ResponseKind = "url"
	}
	if m.ResponseKind != "json" {
		m.Override = appendUnique(m.Override, m.ResponseKind+" response")
	}
}

func analyzePathBuild(m *method, call *ast.CallExpr, definitions map[string]ast.Expr, params map[string]bool, info *types.Info) {
	format := stringConstant(info, call.Args[0])
	if format == "" {
		return
	}
	pathFormat, _, _ := strings.Cut(format, "?")
	argumentCount := strings.Count(pathFormat, "%v")
	if argumentCount == 0 || argumentCount+1 > len(call.Args) {
		return
	}
	parameterIndex := map[string]int{}
	for index, name := range m.ParamNames {
		parameterIndex[name] = index
	}
	build := pathBuild{skeleton: pathSkeleton("/" + strings.TrimPrefix(pathFormat, "/"))}
	for _, argument := range call.Args[1 : argumentCount+1] {
		dependencies := dependentParams(argument, definitions, params, map[string]bool{})
		source := pathSource{index: -1}
		if len(dependencies) == 1 {
			for parameter := range dependencies {
				source.index = parameterIndex[parameter]
			}
		}
		if parameter, field, ok := selectorParameter(argument); ok && parameterIndex[parameter] == source.index {
			source.field = field
		}
		build.sources = append(build.sources, source)
	}
	m.PathBuilds = append(m.PathBuilds, build)
}

func pathSkeleton(path string) string {
	path = placeholderPattern.ReplaceAllString(path, "{}")
	return strings.ReplaceAll(path, "%v", "{}")
}

func analyzeFormattedQuery(m *method, call *ast.CallExpr, definitions map[string]ast.Expr, params map[string]bool, info *types.Info) {
	format := stringConstant(info, call.Args[0])
	before, after, ok := strings.Cut(format, "?")
	if !ok {
		return
	}
	pathArguments := strings.Count(before, "%v")
	queryArgument := 0
	for part := range strings.SplitSeq(after, "&") {
		if !strings.Contains(part, "%v") {
			continue
		}
		argumentIndex := 1 + pathArguments + queryArgument
		queryArgument++
		if argumentIndex >= len(call.Args) {
			continue
		}
		name, _, _ := strings.Cut(part, "=")
		dependencies := dependentParams(call.Args[argumentIndex], definitions, params, map[string]bool{})
		if len(dependencies) != 1 {
			continue
		}
		for parameter := range dependencies {
			m.QueryValues[parameter] = name
		}
	}
}

func firstResultIsBool(signature *types.Signature) bool {
	if signature.Results().Len() == 0 {
		return false
	}
	basic, ok := signature.Results().At(0).Type().Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsBoolean != 0
}

func firstResultIsString(signature *types.Signature) bool {
	if signature.Results().Len() == 0 {
		return false
	}
	basic, ok := signature.Results().At(0).Type().Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsString != 0
}

func firstResultIsRaw(signature *types.Signature) bool {
	if firstResultIsString(signature) {
		return true
	}
	if signature.Results().Len() == 0 {
		return false
	}
	slice, ok := signature.Results().At(0).Type().Underlying().(*types.Slice)
	if !ok {
		return false
	}
	basic, ok := slice.Elem().Underlying().(*types.Basic)
	return ok && basic.Kind() == types.Byte
}

func collectBodyFields(expression ast.Expr, definitions map[string]ast.Expr, params map[string]bool, info *types.Info, fields map[string][]string, seen map[string]bool) {
	switch value := expression.(type) {
	case *ast.Ident:
		if definition, ok := definitions[value.Name]; ok && !seen[value.Name] {
			seen[value.Name] = true
			collectBodyFields(definition, definitions, params, info, fields, seen)
		}
	case *ast.UnaryExpr:
		collectBodyFields(value.X, definitions, params, info, fields, seen)
	case *ast.CompositeLit:
		for _, element := range value.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			wireName := compositeFieldName(value, pair.Key, info)
			if wireName == "" {
				continue
			}
			for parameter := range dependentParams(pair.Value, definitions, params, map[string]bool{}) {
				fields[parameter] = append(fields[parameter], wireName)
			}
		}
	}
}

func compositeFieldName(literal *ast.CompositeLit, key ast.Expr, info *types.Info) string {
	if value := stringConstant(info, key); value != "" {
		return value
	}
	ident, ok := key.(*ast.Ident)
	if !ok {
		return ""
	}
	t := info.TypeOf(literal)
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	structure, ok := t.(*types.Struct)
	if !ok {
		return strings.ToLower(ident.Name)
	}
	for index := 0; index < structure.NumFields(); index++ {
		if structure.Field(index).Name() != ident.Name {
			continue
		}
		name, _, _ := strings.Cut(reflect.StructTag(structure.Tag(index)).Get("json"), ",")
		if name != "" && name != "-" {
			return name
		}
		return strings.ToLower(ident.Name)
	}
	return ""
}

func isStreamSignature(signature *types.Signature) bool {
	return signature.Results().Len() > 0 && types.TypeString(signature.Results().At(0).Type(), nil) == "io.ReadCloser"
}

func isURLSignature(signature *types.Signature) bool {
	return signature.Results().Len() > 0 && types.TypeString(signature.Results().At(0).Type(), nil) == "*net/url.URL"
}

func stringConstant(info *types.Info, expression ast.Expr) string {
	value := info.Types[expression].Value
	if value == nil || value.Kind() != constant.String {
		return ""
	}
	return constant.StringVal(value)
}

func isDownloadSignature(signature *types.Signature) bool {
	if signature.Results().Len() < 3 {
		return false
	}
	return types.TypeString(signature.Results().At(0).Type(), nil) == "io.ReadCloser" &&
		types.TypeString(signature.Results().At(1).Type(), nil) == "string"
}

func calledName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	}
	return ""
}

func isNil(expression ast.Expr) bool {
	ident, ok := expression.(*ast.Ident)
	return ok && ident.Name == "nil"
}

func dependentParams(expression ast.Expr, definitions map[string]ast.Expr, params map[string]bool, seen map[string]bool) map[string]bool {
	result := map[string]bool{}
	ast.Inspect(expression, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if params[ident.Name] {
			result[ident.Name] = true
			return true
		}
		if definition, exists := definitions[ident.Name]; exists && !seen[ident.Name] {
			seen[ident.Name] = true
			for parameter := range dependentParams(definition, definitions, params, seen) {
				result[parameter] = true
			}
		}
		return true
	})
	return result
}

func containsBytesBuffer(function *ast.FuncDecl, info *types.Info) bool {
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		expression, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		t := info.TypeOf(expression)
		if t != nil && types.TypeString(t, nil) == "bytes.Buffer" {
			found = true
			return false
		}
		return true
	})
	return found
}

func render(services []*service) ([]byte, []coverageEntry, error) {
	imports := &importSet{byPath: map[string]string{}}
	routeCounts := map[string]int{}
	for _, service := range services {
		for _, method := range service.Methods {
			for _, route := range method.Routes {
				routeCounts[route.Method+" "+route.Path]++
			}
		}
	}
	var declarations bytes.Buffer
	for _, service := range services {
		fmt.Fprintf(&declarations, "// %sService implements the annotated methods of github.%sService.\n", service.Name, service.Name)
		fmt.Fprintf(&declarations, "type %sService interface {\n", service.Name)
		for _, method := range service.Methods {
			fmt.Fprintln(&declarations, commentBlock(methodDocumentation(method)))
			fmt.Fprintf(&declarations, "\t%s%s\n", method.Name, signatureString(method.Signature, imports))
		}
		fmt.Fprintln(&declarations, "}")
		fmt.Fprintf(&declarations, "\n// Unimplemented%sService may be embedded to implement only selected methods.\n", service.Name)
		fmt.Fprintf(&declarations, "type Unimplemented%sService struct{}\n", service.Name)
		for _, method := range service.Methods {
			renderUnimplemented(&declarations, service.Name, method, imports)
		}
		fmt.Fprintln(&declarations)
	}

	fmt.Fprintln(&declarations, "// Services mirrors the service grouping exposed by github.Client.")
	fmt.Fprintln(&declarations, "type Services struct {")
	for _, service := range services {
		fmt.Fprintf(&declarations, "\t%s %sService\n", service.Name, service.Name)
	}
	fmt.Fprintln(&declarations, "}")
	fmt.Fprintln(&declarations, "\nfunc generatedUnimplementedServices() Services {")
	fmt.Fprintln(&declarations, "\treturn Services{")
	for _, service := range services {
		fmt.Fprintf(&declarations, "\t\t%s: Unimplemented%sService{},\n", service.Name, service.Name)
	}
	fmt.Fprintln(&declarations, "\t}")
	fmt.Fprintln(&declarations, "}")

	var operations bytes.Buffer
	fmt.Fprintln(&operations, "func generatedOperations(services Services) []operation {")
	fmt.Fprintln(&operations, "\tvar operations []operation")
	var coverage []coverageEntry
	for _, service := range services {
		fmt.Fprintf(&operations, "\tif services.%s != nil {\n", service.Name)
		for _, method := range service.Methods {
			for _, route := range method.Routes {
				effective := effectiveRoute(method, route)
				bindings, reasons := operationBindings(method, effective)
				if slices.ContainsFunc(bindings, func(binding renderedBinding) bool { return binding.kind == "bindingAuto" }) {
					reasons = appendUnique(reasons, "automatic parameter binding")
				}
				if reason := unboundParameterReason(method, bindings); reason != "" {
					reasons = appendUnique(reasons, reason)
				}
				allReasons := append([]string{}, method.Override...)
				if effective.Path != route.Path {
					allReasons = appendUnique(allReasons, "actual literal route override")
				}
				for _, reason := range reasons {
					allReasons = appendUnique(allReasons, reason)
				}
				isAlias := routeCounts[route.Method+" "+route.Path] > 1 && slices.Contains(reasons, "unresolved path parameters")
				if !isAlias {
					pattern := serveMuxPath(effective.Path)
					fmt.Fprintf(&operations, "\t\toperations = append(operations, operation{Service: %q, MethodName: %q, HTTPMethod: %q, Path: %q, Pattern: %q, ResponseKind: %q, Accept: %#v, Direct: %t, Source: %q, Impl: services.%s, Bindings: []binding{", service.Name, method.Name, route.Method, effective.Path, pattern, method.ResponseKind, method.Accept, method.Direct, method.Source, service.Name)
					for _, binding := range bindings {
						fmt.Fprintf(&operations, "{Kind: %s, Index: %d, Name: %q, Field: %q},", binding.kind, binding.index, binding.name, binding.field)
					}
					fmt.Fprintln(&operations, "}})")
				}
				status := "generated-clean"
				if isAlias {
					status = "generated-alias"
					allReasons = appendUnique(allReasons, "canonical shared route used")
				} else if len(allReasons) > 0 {
					status = "generated-with-override"
				}
				coverage = append(coverage, coverageEntry{Service: service.Name, Method: method.Name, HTTP: route.Method, Path: route.Path, Status: status, Reasons: allReasons, Source: method.Source})
			}
		}
		fmt.Fprintln(&operations, "\t}")
	}
	fmt.Fprintln(&operations, "\treturn operations")
	fmt.Fprintln(&operations, "}")
	markSharedOperations(coverage)

	var source bytes.Buffer
	fmt.Fprintln(&source, "// Code generated by cmd/gen-server; DO NOT EDIT.")
	fmt.Fprintln(&source, "package githubserver")
	if len(imports.byPath) > 0 {
		fmt.Fprintln(&source, "import (")
		paths := make([]string, 0, len(imports.byPath))
		for path := range imports.byPath {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			fmt.Fprintf(&source, "\t%s %q\n", imports.byPath[path], path)
		}
		fmt.Fprintln(&source, ")")
	}
	source.Write(declarations.Bytes())
	source.Write(operations.Bytes())
	return source.Bytes(), coverage, nil
}

func unboundParameterReason(method *method, bindings []renderedBinding) string {
	bound := map[int]bool{}
	for _, binding := range bindings {
		bound[binding.index] = true
	}
	var names []string
	for index := 1; index < method.Signature.Params().Len(); index++ {
		if !bound[index] {
			name := method.ParamNames[index]
			if name == "" {
				name = fmt.Sprintf("arg%d", index)
			}
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "unbound parameters: " + strings.Join(names, ",")
}

func markSharedOperations(coverage []coverageEntry) {
	counts := map[string]int{}
	for _, operation := range coverage {
		counts[operation.HTTP+" "+operation.Path]++
	}
	for i := range coverage {
		key := coverage[i].HTTP + " " + coverage[i].Path
		if counts[key] > 1 && coverage[i].Status != "generated-alias" {
			coverage[i].Status = "generated-with-override"
			coverage[i].Reasons = appendUnique(coverage[i].Reasons, "shared HTTP operation")
		}
		if coverage[i].Path == "/hub" {
			coverage[i].Status = "generated-with-override"
			coverage[i].Reasons = appendUnique(coverage[i].Reasons, "form-discriminated operation")
		}
	}
}

func signatureString(signature *types.Signature, imports *importSet) string {
	text := types.TypeString(signature, imports.qualifier)
	return strings.TrimPrefix(text, "func")
}

func (imports *importSet) qualifier(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}
	alias := pkg.Name()
	for path, existing := range imports.byPath {
		if existing == alias && path != pkg.Path() {
			alias = strings.ReplaceAll(pkg.Path(), "/", "_")
		}
	}
	imports.byPath[pkg.Path()] = alias
	return alias
}

func renderUnimplemented(output *bytes.Buffer, serviceName string, method *method, imports *importSet) {
	name := method.Name
	signature := method.Signature
	fmt.Fprintf(output, "func (Unimplemented%sService) %s%s {\n", serviceName, name, signatureString(signature, imports))
	returns := signature.Results()
	if returns.Len() == 0 {
		fmt.Fprintln(output, "\treturn")
		fmt.Fprintln(output, "}")
		return
	}
	values := make([]string, returns.Len())
	for i := 0; i < returns.Len(); i++ {
		t := returns.At(i).Type()
		if types.Implements(t, types.Universe.Lookup("error").Type().Underlying().(*types.Interface)) {
			values[i] = "ErrNotImplemented"
			continue
		}
		name := fmt.Sprintf("zero%d", i)
		fmt.Fprintf(output, "\tvar %s %s\n", name, types.TypeString(t, imports.qualifier))
		values[i] = name
	}
	fmt.Fprintf(output, "\treturn %s\n", strings.Join(values, ", "))
	fmt.Fprintln(output, "}")
}

type renderedBinding struct {
	kind  string
	index int
	name  string
	field string
}

func operationBindings(method *method, route route) ([]renderedBinding, []string) {
	if method.Name == "GetArchiveLink" {
		format := "tarball"
		if strings.Contains(route.Path, "/zipball/") {
			format = "zipball"
		}
		return appendNonPathBindings(method, []renderedBinding{
			{kind: "bindingContext", index: 0},
			{kind: "bindingPath", index: 1, name: "p0"},
			{kind: "bindingPath", index: 2, name: "p1"},
			{kind: "bindingConstant", index: 3, name: format},
			{kind: "bindingPathField", index: 4, name: "p2", field: "Ref"},
		}, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true}), []string{"archive format and ref override"}
	}
	if strings.Contains(route.Path, "{basehead}") {
		return appendNonPathBindings(method, []renderedBinding{
			{kind: "bindingContext", index: 0},
			{kind: "bindingPath", index: 1, name: "p0"},
			{kind: "bindingPath", index: 2, name: "p1"},
			{kind: "bindingCompositePart", index: 3, name: "p2", field: "0"},
			{kind: "bindingCompositePart", index: 4, name: "p2", field: "1"},
		}, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true}), []string{"composite path parameter"}
	}
	bindings := []renderedBinding{{kind: "bindingContext", index: 0}}
	bound := map[int]bool{0: true}
	placeholders := placeholderPattern.FindAllStringSubmatch(route.Path, -1)
	var reasons []string
	mappedFromBuild := false
	for _, build := range method.PathBuilds {
		if build.skeleton != pathSkeleton(route.Path) || len(build.sources) != len(placeholders) {
			continue
		}
		valid := true
		for _, source := range build.sources {
			valid = valid && source.index > 0
		}
		if !valid {
			continue
		}
		for index, source := range build.sources {
			kind := "bindingPath"
			if source.field != "" {
				kind = "bindingPathField"
			}
			bindings = append(bindings, renderedBinding{kind: kind, index: source.index, name: fmt.Sprintf("p%d", index), field: source.field})
			if source.field == "" {
				bound[source.index] = true
			}
		}
		mappedFromBuild = true
		break
	}
	if !mappedFromBuild {
		candidates := scalarCandidates(method.Signature)
		if len(placeholders) > len(candidates) {
			reasons = append(reasons, "unresolved path parameters")
		}
		for i := range placeholders {
			if i >= len(candidates) {
				break
			}
			index := candidates[i]
			bindings = append(bindings, renderedBinding{kind: "bindingPath", index: index, name: fmt.Sprintf("p%d", i)})
			bound[index] = true
		}
		for placeholderIndex, candidate := range structuredPathCandidates(method) {
			index := len(candidates) + placeholderIndex
			if index >= len(placeholders) {
				break
			}
			if bound[candidate.index] {
				continue
			}
			bindings = append(bindings, renderedBinding{kind: "bindingPathField", index: candidate.index, name: fmt.Sprintf("p%d", index), field: candidate.field})
		}
		if len(placeholders) <= len(candidates)+len(structuredPathCandidates(method)) {
			reasons = slices.DeleteFunc(reasons, func(reason string) bool { return reason == "unresolved path parameters" })
		}
	}
	bindings = appendNonPathBindings(method, bindings, bound)
	if strings.Contains(route.Path, "{basehead}") {
		reasons = append(reasons, "composite path parameter")
	}
	return bindings, reasons
}

func appendNonPathBindings(method *method, bindings []renderedBinding, bound map[int]bool) []renderedBinding {
	for index, name := range method.ParamNames {
		if bound[index] {
			continue
		}
		switch {
		case method.BindsAppJWT && name == "appJWT":
			bindings = append(bindings, renderedBinding{kind: "bindingAuthorization", index: index})
			bound[index] = true
		case strings.HasSuffix(types.TypeString(method.Signature.Params().At(index).Type(), nil), ".RawOptions"):
			bindings = append(bindings, renderedBinding{kind: "bindingRawOptions", index: index})
			bound[index] = true
		case method.QueryValues[name] != "":
			bindings = append(bindings, renderedBinding{kind: "bindingQueryValue", index: index, name: method.QueryValues[name]})
			bound[index] = true
		case method.QueryParams[name]:
			bindings = append(bindings, renderedBinding{kind: "bindingQuery", index: index})
			bound[index] = true
		case method.BodyParams[name]:
			bindings = append(bindings, renderedBinding{kind: "bindingJSON", index: index, field: method.BodyFields[name]})
			bound[index] = true
		case method.UploadParams[name] && isReader(method.Signature.Params().At(index).Type()):
			bindings = append(bindings, renderedBinding{kind: "bindingRequestBody", index: index})
			bound[index] = true
		case method.UploadParams[name] && types.TypeString(method.Signature.Params().At(index).Type(), nil) == "*os.File":
			bindings = append(bindings, renderedBinding{kind: "bindingTempFile", index: index})
			bound[index] = true
		case method.UploadParams[name] && name == "size":
			bindings = append(bindings, renderedBinding{kind: "bindingContentLength", index: index})
			bound[index] = true
		case method.UploadParams[name] && name == "mediaType":
			bindings = append(bindings, renderedBinding{kind: "bindingContentType", index: index})
			bound[index] = true
		}
		if !bound[index] {
			bindings = append(bindings, renderedBinding{kind: "bindingAuto", index: index, name: name})
			bound[index] = true
		}
	}
	return bindings
}

type pathFieldCandidate struct {
	index int
	field string
}

func structuredPathCandidates(method *method) []pathFieldCandidate {
	parameterIndex := map[string]int{}
	for index, name := range method.ParamNames {
		parameterIndex[name] = index
	}
	var candidates []pathFieldCandidate
	ast.Inspect(method.Decl.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || calledName(call.Fun) != "Sprintf" {
			return true
		}
		for _, argument := range call.Args[1:] {
			parameter, field, ok := selectorParameter(argument)
			if !ok {
				continue
			}
			index, exists := parameterIndex[parameter]
			if !exists {
				continue
			}
			candidate := pathFieldCandidate{index: index, field: field}
			if !slices.Contains(candidates, candidate) {
				candidates = append(candidates, candidate)
			}
		}
		return true
	})
	return candidates
}

func selectorParameter(expression ast.Expr) (string, string, bool) {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return selectorParameter(value.X)
	case *ast.ParenExpr:
		return selectorParameter(value.X)
	case *ast.SelectorExpr:
		root := value.X
		for {
			selector, ok := root.(*ast.SelectorExpr)
			if !ok {
				break
			}
			root = selector.X
		}
		if ident, ok := root.(*ast.Ident); ok {
			return ident.Name, value.Sel.Name, true
		}
	}
	return "", "", false
}

func scalarCandidates(signature *types.Signature) []int {
	var indices []int
	for i := 1; i < signature.Params().Len(); i++ {
		t := signature.Params().At(i).Type()
		basic, ok := t.Underlying().(*types.Basic)
		if !ok || basic.Info()&types.IsBoolean != 0 {
			continue
		}
		if basic.Info()&(types.IsString|types.IsInteger|types.IsUnsigned) != 0 {
			indices = append(indices, i)
		}
	}
	return indices
}

func isReader(t types.Type) bool {
	return strings.Contains(types.TypeString(t, nil), "io.Reader")
}

func serveMuxPath(path string) string {
	index := 0
	return placeholderPattern.ReplaceAllStringFunc(path, func(placeholder string) string {
		name := fmt.Sprintf("{p%d}", index)
		if placeholder == "{path}" && strings.HasSuffix(path, placeholder) {
			name = fmt.Sprintf("{p%d...}", index)
		}
		index++
		return name
	})
}

func appendUnique(values []string, value string) []string {
	if !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func checkFile(path string, expected []byte) {
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, expected) {
		fatal(fmt.Errorf("%s is out of date; run the generator", path))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
