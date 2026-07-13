// Package githubserver turns implementations of the services in go-github into
// an HTTP handler for the corresponding GitHub REST routes.
package githubserver

//go:generate go run ./cmd/gen-server -output zz_generated.go -coverage coverage.json

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v89/github"
)

// ErrNotImplemented is returned by generated Unimplemented<Service> values.
var ErrNotImplemented = errors.New("github service method is not implemented")

// Authenticator delegates interpretation and validation of opaque GitHub
// credentials to the application. A returned context is installed on the
// request before a service method is called.
type Authenticator interface {
	AuthenticateWithPAT(context.Context, string) (context.Context, error)
	AuthenticateWithInstallationToken(context.Context, string) (context.Context, error)
}

// New constructs a GitHub-compatible HTTP mux from the supplied service
// implementations. Nil service fields simply leave their routes unregistered.
func New(services Services, authenticator Authenticator) *http.ServeMux {
	mux := http.NewServeMux()
	registerOperations(mux, authenticator, generatedOperations(services))
	return mux
}

type bindingKind uint8

const (
	bindingContext bindingKind = iota
	bindingPath
	bindingQuery
	bindingJSON
	bindingRequestBody
	bindingTempFile
	bindingContentLength
	bindingContentType
	bindingPathField
	bindingRawOptions
	bindingConstant
	bindingCompositePart
	bindingQueryValue
	bindingAuto
)

type binding struct {
	Kind  bindingKind
	Index int
	Name  string
	Field string
}

type operation struct {
	Service      string
	MethodName   string
	HTTPMethod   string
	Path         string
	Pattern      string
	Bindings     []binding
	ResponseKind string
	Accept       []string
	Direct       bool
	Source       string
	Impl         any
}

func registerOperations(mux *http.ServeMux, authenticator Authenticator, operations []operation) {
	routes := buildRouteGroups(operations)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, route := range routes {
			if values, matches := route.match(r); matches {
				for name, value := range values {
					r.SetPathValue(name, value)
				}
				serveOperationGroup(w, r, authenticator, route.operations)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func buildRouteGroups(operations []operation) []routeGroup {
	groups := make(map[string][]operation)
	for _, op := range operations {
		if op.Impl == nil {
			continue
		}
		key := op.HTTPMethod + " " + op.Pattern
		groups[key] = append(groups[key], op)
	}
	routes := make([]routeGroup, 0, len(groups))
	for _, ops := range groups {
		routes = append(routes, routeGroup{operations: ops, segments: splitPath(ops[0].Pattern)})
	}
	slices.SortStableFunc(routes, func(a, b routeGroup) int {
		if difference := b.literalSegments() - a.literalSegments(); difference != 0 {
			return difference
		}
		return len(b.segments) - len(a.segments)
	})
	return routes
}

type routeGroup struct {
	operations []operation
	segments   []string
}

func (g routeGroup) literalSegments() int {
	count := 0
	for _, segment := range g.segments {
		if !strings.HasPrefix(segment, "{") {
			count++
		}
	}
	return count
}

func (g routeGroup) match(r *http.Request) (map[string]string, bool) {
	op := g.operations[0]
	if r.Method != op.HTTPMethod {
		return nil, false
	}
	requestPath := strings.TrimPrefix(r.URL.Path, "/api/v3")
	requestPath = strings.TrimPrefix(requestPath, "/api/uploads")
	if requestPath == "" {
		requestPath = "/"
	}
	actual := splitPath(requestPath)
	values := map[string]string{}
	for index, expected := range g.segments {
		isWildcard := strings.HasPrefix(expected, "{")
		isRemainder := strings.HasSuffix(expected, "...}")
		if isRemainder {
			if index >= len(actual) {
				return nil, false
			}
			name := strings.TrimSuffix(strings.TrimPrefix(expected, "{"), "...}")
			values[name] = strings.Join(actual[index:], "/")
			return values, true
		}
		if index >= len(actual) {
			return nil, false
		}
		if isWildcard {
			name := strings.TrimSuffix(strings.TrimPrefix(expected, "{"), "}")
			values[name] = actual[index]
			continue
		}
		if expected != actual[index] {
			return nil, false
		}
	}
	return values, len(actual) == len(g.segments)
}

func splitPath(path string) []string {
	if path == "/" {
		return nil
	}
	return strings.Split(strings.Trim(path, "/"), "/")
}

func serveOperationGroup(w http.ResponseWriter, r *http.Request, authenticator Authenticator, ops []operation) {
	ctx, ok := authenticate(w, r, authenticator)
	if !ok {
		return
	}
	r = r.WithContext(ctx)

	candidates := orderedOperations(r, ops)
	if len(ops) > 1 && ops[0].Path == "/hub" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}
		mode := r.Form.Get("hub.mode")
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.MethodName, mode) {
				candidates = []operation{candidate}
				break
			}
		}
	}
	var invokeErr error
	if len(candidates) == 1 {
		invokeErr = invokeOperation(w, r, candidates[0])
	} else {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		for _, op := range candidates {
			r.Body = io.NopCloser(bytes.NewReader(payload))
			invokeErr = invokeOperation(w, r, op)
			if !errors.Is(invokeErr, ErrNotImplemented) {
				break
			}
		}
	}
	if invokeErr != nil {
		if errors.Is(invokeErr, ErrNotImplemented) {
			http.Error(w, invokeErr.Error(), http.StatusNotImplemented)
			return
		}
		var requestErr *requestError
		if errors.As(invokeErr, &requestErr) {
			http.Error(w, requestErr.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, invokeErr.Error(), http.StatusInternalServerError)
	}
}

func orderedOperations(r *http.Request, operations []operation) []operation {
	ordered := slices.Clone(operations)
	accept := r.Header.Get("Accept")
	slices.SortStableFunc(ordered, func(a, b operation) int {
		return operationScore(b, accept) - operationScore(a, accept)
	})
	return ordered
}

func operationScore(op operation, accept string) int {
	score := 0
	for _, mediaType := range op.Accept {
		if headerContains(accept, mediaType) {
			score += 100
		}
	}
	if len(op.Accept) == 0 && (accept == "" || strings.Contains(accept, "json") || accept == "*/*") {
		score += 50
	}
	if op.ResponseKind == "json" {
		score += 10
	}
	if op.Direct {
		score += 20
	}
	return score
}

func headerContains(header, value string) bool {
	for _, item := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(item, ";", 2)[0]), value) {
			return true
		}
	}
	return false
}

func authenticate(w http.ResponseWriter, r *http.Request, authenticator Authenticator) (context.Context, bool) {
	if authenticator == nil {
		return r.Context(), true
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return r.Context(), true
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || (!strings.EqualFold(parts[0], "Bearer") && !strings.EqualFold(parts[0], "token")) {
		http.Error(w, "unsupported authorization header", http.StatusUnauthorized)
		return nil, false
	}
	token := parts[1]
	var (
		ctx context.Context
		err error
	)
	switch {
	case strings.HasPrefix(token, "ghp_"), strings.HasPrefix(token, "github_pat_"):
		ctx, err = authenticator.AuthenticateWithPAT(r.Context(), token)
	case strings.HasPrefix(token, "ghs_"):
		ctx, err = authenticator.AuthenticateWithInstallationToken(r.Context(), token)
	default:
		http.Error(w, "unsupported GitHub token type", http.StatusUnauthorized)
		return nil, false
	}
	if err != nil || ctx == nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return nil, false
	}
	return ctx, true
}

type requestError struct{ err error }

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

func invokeOperation(w http.ResponseWriter, r *http.Request, op operation) error {
	method := reflect.ValueOf(op.Impl).MethodByName(op.MethodName)
	if !method.IsValid() {
		return fmt.Errorf("%s.%s: %w", op.Service, op.MethodName, ErrNotImplemented)
	}
	args := make([]reflect.Value, method.Type().NumIn())
	for i := range args {
		args[i] = reflect.Zero(method.Type().In(i))
	}

	var (
		body      []byte
		tempFiles []*os.File
	)
	defer func() {
		for _, file := range tempFiles {
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
		}
	}()
	for _, b := range op.Bindings {
		t := method.Type().In(b.Index)
		switch b.Kind {
		case bindingContext:
			args[b.Index] = reflect.ValueOf(r.Context())
		case bindingPath:
			value, err := parseScalar(r.PathValue(b.Name), t)
			if err != nil {
				return &requestError{fmt.Errorf("path parameter %s: %w", b.Name, err)}
			}
			args[b.Index] = value
		case bindingConstant:
			value, err := parseScalar(b.Name, t)
			if err != nil {
				return fmt.Errorf("constant parameter for %s.%s: %w", op.Service, op.MethodName, err)
			}
			args[b.Index] = value
		case bindingCompositePart:
			parts := strings.SplitN(r.PathValue(b.Name), "...", 2)
			part, err := strconv.Atoi(b.Field)
			if err != nil || len(parts) != 2 || part >= len(parts) {
				return &requestError{fmt.Errorf("path parameter %s must contain base...head", b.Name)}
			}
			value, err := parseScalar(parts[part], t)
			if err != nil {
				return &requestError{fmt.Errorf("path parameter %s: %w", b.Name, err)}
			}
			args[b.Index] = value
		case bindingPathField:
			value := args[b.Index]
			if value.Kind() == reflect.Pointer {
				if value.IsNil() {
					value = reflect.New(value.Type().Elem())
					args[b.Index] = value
				}
				value = value.Elem()
			}
			field := value.FieldByName(b.Field)
			if !field.IsValid() || !field.CanSet() {
				return fmt.Errorf("%s.%s: cannot bind path to field %s", op.Service, op.MethodName, b.Field)
			}
			parsed, err := parseScalar(r.PathValue(b.Name), field.Type())
			if err != nil {
				return &requestError{fmt.Errorf("path parameter %s: %w", b.Name, err)}
			}
			field.Set(parsed)
		case bindingQuery:
			value := reflect.New(t)
			if t.Kind() == reflect.Pointer {
				if !args[b.Index].IsNil() {
					value = args[b.Index]
				} else {
					value = reflect.New(t.Elem())
				}
				if err := decodeQuery(value.Elem(), r.URL.Query()); err != nil {
					return &requestError{err}
				}
				args[b.Index] = value
			} else {
				if err := decodeQuery(value.Elem(), r.URL.Query()); err != nil {
					return &requestError{err}
				}
				args[b.Index] = value.Elem()
			}
		case bindingQueryValue:
			value, err := parseScalar(r.URL.Query().Get(b.Name), t)
			if err != nil {
				return &requestError{fmt.Errorf("query parameter %s: %w", b.Name, err)}
			}
			args[b.Index] = value
		case bindingJSON:
			if body == nil {
				var err error
				body, err = io.ReadAll(r.Body)
				if err != nil {
					return &requestError{err}
				}
			}
			decodeBody := body
			if b.Field != "" {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(body, &object); err != nil {
					return &requestError{fmt.Errorf("JSON body: %w", err)}
				}
				field, exists := object[b.Field]
				if !exists {
					return &requestError{fmt.Errorf("JSON body is missing %q", b.Field)}
				}
				decodeBody = field
			}
			if len(bytes.TrimSpace(decodeBody)) == 0 {
				continue
			}
			value := reflect.New(t)
			decodeTarget := value.Interface()
			if t.Kind() == reflect.Pointer {
				if !args[b.Index].IsNil() {
					value = args[b.Index]
				} else {
					value = reflect.New(t.Elem())
				}
				decodeTarget = value.Interface()
			} else if !args[b.Index].IsZero() {
				value.Elem().Set(args[b.Index])
			}
			if err := json.Unmarshal(decodeBody, decodeTarget); err != nil {
				return &requestError{fmt.Errorf("JSON body: %w", err)}
			}
			if t.Kind() == reflect.Pointer {
				args[b.Index] = value
			} else {
				args[b.Index] = value.Elem()
			}
		case bindingRequestBody:
			bodyValue := reflect.ValueOf(r.Body)
			if bodyValue.Type().AssignableTo(t) {
				args[b.Index] = bodyValue
			}
		case bindingTempFile:
			file, err := os.CreateTemp("", "go-github-server-upload-*")
			if err != nil {
				return err
			}
			tempFiles = append(tempFiles, file)
			if _, err := io.Copy(file, r.Body); err != nil {
				return &requestError{err}
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			args[b.Index] = reflect.ValueOf(file)
		case bindingContentLength:
			args[b.Index] = reflect.ValueOf(r.ContentLength).Convert(t)
		case bindingContentType:
			args[b.Index] = reflect.ValueOf(r.Header.Get("Content-Type")).Convert(t)
		case bindingRawOptions:
			value := reflect.New(t).Elem()
			kind := ""
			switch {
			case strings.Contains(r.Header.Get("Accept"), ".diff"):
				kind = "diff"
			case strings.Contains(r.Header.Get("Accept"), ".patch"):
				kind = "patch"
			}
			if field := value.FieldByName("Type"); field.IsValid() && field.CanSet() {
				switch field.Kind() {
				case reflect.String:
					field.SetString(kind)
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					switch kind {
					case "diff":
						field.SetUint(1)
					case "patch":
						field.SetUint(2)
					}
				}
			}
			args[b.Index] = value
		case bindingAuto:
			value, err := autoBind(r, t, b.Name, &body, args[b.Index])
			if err != nil {
				return &requestError{fmt.Errorf("parameter %s: %w", b.Name, err)}
			}
			args[b.Index] = value
		}
	}
	if op.Path == "/hub" {
		if err := populateHubArgs(args, r.Form); err != nil {
			return &requestError{err}
		}
	}

	results := method.Call(args)
	return writeResults(w, op, results)
}

func autoBind(r *http.Request, target reflect.Type, name string, body *[]byte, existing reflect.Value) (reflect.Value, error) {
	value := existing
	if !value.IsValid() {
		value = reflect.Zero(target)
	}
	if name == "maxRedirects" || name == "followRedirectsClient" {
		return value, nil
	}
	if name == "lastSHA" {
		header := strings.Trim(r.Header.Get("If-None-Match"), `"`)
		if header != "" {
			return parseScalar(header, target)
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		if target.Kind() == reflect.Struct || (target.Kind() == reflect.Pointer && target.Elem().Kind() == reflect.Struct) {
			pointer := reflect.New(target)
			if target.Kind() == reflect.Pointer {
				pointer = reflect.New(target.Elem())
				if err := decodeQuery(pointer.Elem(), r.URL.Query()); err != nil {
					return reflect.Value{}, err
				}
				setPublicOnly(pointer.Elem(), r.URL.Path)
				return pointer, nil
			}
			if err := decodeQuery(pointer.Elem(), r.URL.Query()); err != nil {
				return reflect.Value{}, err
			}
			setPublicOnly(pointer.Elem(), r.URL.Path)
			return pointer.Elem(), nil
		}
		if target.Kind() == reflect.Bool && strings.Contains(r.URL.Path, "/public") {
			value := reflect.New(target).Elem()
			value.SetBool(true)
			return value, nil
		}
		keys := []string{snakeCase(name), name}
		if name == "query" {
			keys = append([]string{"q"}, keys...)
		}
		for _, key := range keys {
			if input, exists := r.URL.Query()[key]; exists && len(input) > 0 {
				return parseScalar(input[len(input)-1], target)
			}
		}
		return value, nil
	}
	if *body == nil {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			return reflect.Value{}, err
		}
		*body = payload
	}
	if len(bytes.TrimSpace(*body)) == 0 {
		return value, nil
	}
	decoded, err := decodeJSONValue(*body, target, existing)
	if err == nil {
		return decoded, nil
	}
	var object map[string]json.RawMessage
	if objectErr := json.Unmarshal(*body, &object); objectErr != nil {
		return reflect.Value{}, err
	}
	normalizedName := strings.TrimSuffix(normalizeName(name), "s")
	for key, raw := range object {
		normalizedKey := strings.TrimSuffix(normalizeName(key), "s")
		if normalizedKey == normalizedName || strings.Contains(normalizedKey, normalizedName) || strings.Contains(normalizedName, normalizedKey) {
			if decoded, fieldErr := decodeJSONValue(raw, target, existing); fieldErr == nil {
				return decoded, nil
			}
		}
	}
	var matches []reflect.Value
	for _, raw := range object {
		if decoded, fieldErr := decodeJSONValue(raw, target, existing); fieldErr == nil {
			matches = append(matches, decoded)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return reflect.Value{}, err
}

func decodeJSONValue(body []byte, target reflect.Type, existing reflect.Value) (reflect.Value, error) {
	value := reflect.New(target)
	decodeTarget := value.Interface()
	if target.Kind() == reflect.Pointer {
		if existing.IsValid() && !existing.IsNil() {
			value = existing
		} else {
			value = reflect.New(target.Elem())
		}
		decodeTarget = value.Interface()
	}
	if err := json.Unmarshal(body, decodeTarget); err != nil {
		return reflect.Value{}, err
	}
	if target.Kind() == reflect.Pointer {
		return value, nil
	}
	return value.Elem(), nil
}

func setPublicOnly(value reflect.Value, path string) {
	if strings.Contains(path, "/public") {
		if field := value.FieldByName("PublicOnly"); field.IsValid() && field.CanSet() && field.Kind() == reflect.Bool {
			field.SetBool(true)
		}
	}
}

func snakeCase(name string) string {
	var builder strings.Builder
	for index, r := range name {
		if index > 0 && r >= 'A' && r <= 'Z' {
			builder.WriteByte('_')
		}
		builder.WriteRune(r)
	}
	return strings.ToLower(builder.String())
}

func normalizeName(name string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(name))
}

func populateHubArgs(args []reflect.Value, form url.Values) error {
	topic, err := url.Parse(form.Get("hub.topic"))
	if err != nil {
		return fmt.Errorf("hub.topic: %w", err)
	}
	segments := strings.Split(strings.Trim(topic.Path, "/"), "/")
	if len(segments) != 4 || segments[2] != "events" || len(args) < 6 {
		return errors.New("hub.topic must be a GitHub repository event URL")
	}
	args[1] = reflect.ValueOf(segments[0])
	args[2] = reflect.ValueOf(segments[1])
	args[3] = reflect.ValueOf(segments[3])
	args[4] = reflect.ValueOf(form.Get("hub.callback"))
	if secret := form.Get("hub.secret"); secret != "" {
		args[5] = reflect.ValueOf([]byte(secret))
	}
	return nil
}

func writeResults(w http.ResponseWriter, op operation, results []reflect.Value) error {
	var (
		bodyValues []any
		response   *github.Response
	)
	for _, result := range results {
		if result.Type().Implements(reflect.TypeFor[error]()) {
			if !result.IsNil() {
				return result.Interface().(error)
			}
			continue
		}
		if result.Type() == reflect.TypeFor[*github.Response]() {
			if !result.IsNil() {
				response = result.Interface().(*github.Response)
			}
			continue
		}
		if isNilable(result.Kind()) && result.IsNil() {
			continue
		}
		bodyValues = append(bodyValues, result.Interface())
	}

	status := 0
	if response != nil && response.Response != nil {
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		status = response.StatusCode
	}
	if status == 0 {
		switch {
		case op.ResponseKind == "bool" && len(bodyValues) == 1:
			if bodyValues[0].(bool) {
				status = http.StatusNoContent
			} else {
				status = http.StatusNotFound
			}
		case op.ResponseKind == "url":
			status = http.StatusFound
		case len(bodyValues) == 0:
			status = http.StatusNoContent
		default:
			status = http.StatusOK
		}
	}

	switch op.ResponseKind {
	case "bool":
		w.WriteHeader(status)
		return nil
	case "raw":
		w.WriteHeader(status)
		if len(bodyValues) == 0 {
			return nil
		}
		switch value := bodyValues[0].(type) {
		case string:
			_, _ = io.WriteString(w, value)
		case []byte:
			_, _ = w.Write(value)
		default:
			return fmt.Errorf("%s.%s returned %T for a raw response", op.Service, op.MethodName, value)
		}
		return nil
	case "url":
		if len(bodyValues) > 0 {
			switch location := bodyValues[0].(type) {
			case *url.URL:
				if location != nil {
					w.Header().Set("Location", location.String())
				}
			case string:
				w.Header().Set("Location", location)
			}
		}
		w.WriteHeader(status)
		return nil
	case "download":
		for _, body := range bodyValues {
			switch value := body.(type) {
			case io.ReadCloser:
				defer func() { _ = value.Close() }()
				w.WriteHeader(http.StatusOK)
				_, err := io.Copy(w, value)
				return err
			case string:
				if value != "" {
					w.Header().Set("Location", value)
					w.WriteHeader(http.StatusFound)
					return nil
				}
			}
		}
		w.WriteHeader(status)
		return nil
	case "stream":
		for _, body := range bodyValues {
			if reader, ok := body.(io.ReadCloser); ok {
				defer func() { _ = reader.Close() }()
				w.WriteHeader(status)
				_, err := io.Copy(w, reader)
				return err
			}
		}
		w.WriteHeader(status)
		return nil
	}

	if len(bodyValues) == 0 {
		w.WriteHeader(status)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	if len(bodyValues) == 1 {
		return encoder.Encode(bodyValues[0])
	}
	return encoder.Encode(bodyValues)
}

func isNilable(kind reflect.Kind) bool {
	return kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface || kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice
}

func parseScalar(input string, target reflect.Type) (reflect.Value, error) {
	if target.Kind() == reflect.Pointer {
		value, err := parseScalar(input, target.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		pointer := reflect.New(target.Elem())
		pointer.Elem().Set(value)
		return pointer, nil
	}
	value := reflect.New(target).Elem()
	if target == reflect.TypeFor[time.Time]() {
		for _, layout := range []string{time.RFC3339, time.DateOnly} {
			parsed, err := time.Parse(layout, input)
			if err == nil {
				value.Set(reflect.ValueOf(parsed))
				return value, nil
			}
		}
		return reflect.Value{}, fmt.Errorf("invalid time %q", input)
	}
	if target.Kind() == reflect.Struct {
		if field := value.FieldByName("Time"); field.IsValid() && field.CanSet() && field.Type() == reflect.TypeFor[time.Time]() {
			for _, layout := range []string{time.RFC3339, time.DateOnly} {
				parsed, err := time.Parse(layout, input)
				if err == nil {
					field.Set(reflect.ValueOf(parsed))
					return value, nil
				}
			}
			return reflect.Value{}, fmt.Errorf("invalid time %q", input)
		}
	}
	if value.CanAddr() && value.Addr().Type().Implements(reflect.TypeFor[encoding.TextUnmarshaler]()) {
		if err := value.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(input)); err != nil {
			return reflect.Value{}, err
		}
		return value, nil
	}
	switch target.Kind() {
	case reflect.String:
		value.SetString(input)
	case reflect.Bool:
		if input == "1" {
			value.SetBool(true)
			return value, nil
		}
		if input == "0" {
			value.SetBool(false)
			return value, nil
		}
		parsed, err := strconv.ParseBool(input)
		if err != nil {
			return reflect.Value{}, err
		}
		value.SetBool(parsed)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(input, 10, target.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		value.SetInt(parsed)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		parsed, err := strconv.ParseUint(input, 10, target.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		value.SetUint(parsed)
	default:
		return reflect.Value{}, fmt.Errorf("unsupported scalar type %v", target)
	}
	return value, nil
}

func decodeQuery(target reflect.Value, values url.Values) error {
	if target.Kind() == reflect.Pointer {
		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}
		return decodeQuery(target.Elem(), values)
	}
	if target.Kind() != reflect.Struct {
		return fmt.Errorf("query options must be a struct, got %v", target.Type())
	}
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		definition := target.Type().Field(i)
		if definition.Anonymous && definition.Tag.Get("url") == "" {
			if err := decodeQuery(field, values); err != nil {
				return err
			}
			continue
		}
		tag := definition.Tag.Get("url")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		items, exists := values[name]
		if !exists || len(items) == 0 || !field.CanSet() {
			continue
		}
		if field.Kind() == reflect.Slice {
			slice := reflect.MakeSlice(field.Type(), 0, len(items))
			for _, item := range items {
				parsed, err := parseScalar(item, field.Type().Elem())
				if err != nil {
					return fmt.Errorf("query parameter %s: %w", name, err)
				}
				slice = reflect.Append(slice, parsed)
			}
			field.Set(slice)
			continue
		}
		parsed, err := parseScalar(items[len(items)-1], field.Type())
		if err != nil {
			return fmt.Errorf("query parameter %s: %w", name, err)
		}
		field.Set(parsed)
	}
	return nil
}
