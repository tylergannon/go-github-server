package githubserver

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryHooksStub struct {
	UnimplementedRepositoriesService
	create func(context.Context, string, string, *github.Hook) (*github.Hook, *github.Response, error)
	list   func(context.Context, string, string, *github.ListOptions) ([]*github.Hook, *github.Response, error)
	get    func(context.Context, string, string, int64) (*github.Hook, *github.Response, error)
	edit   func(context.Context, string, string, int64, *github.Hook) (*github.Hook, *github.Response, error)
	delete func(context.Context, string, string, int64) (*github.Response, error)
	sub    func(context.Context, string, string, string, string, []byte) (*github.Response, error)
	unsub  func(context.Context, string, string, string, string, []byte) (*github.Response, error)
}

func (s repositoryHooksStub) CreateHook(ctx context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return s.create(ctx, owner, repo, hook)
}

func (s repositoryHooksStub) ListHooks(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
	return s.list(ctx, owner, repo, opts)
}

func (s repositoryHooksStub) GetHook(ctx context.Context, owner, repo string, id int64) (*github.Hook, *github.Response, error) {
	return s.get(ctx, owner, repo, id)
}

func (s repositoryHooksStub) EditHook(ctx context.Context, owner, repo string, id int64, body *github.Hook) (*github.Hook, *github.Response, error) {
	return s.edit(ctx, owner, repo, id, body)
}

func (s repositoryHooksStub) DeleteHook(ctx context.Context, owner, repo string, id int64) (*github.Response, error) {
	return s.delete(ctx, owner, repo, id)
}

func (s repositoryHooksStub) Subscribe(ctx context.Context, owner, repo, event, callback string, secret []byte) (*github.Response, error) {
	return s.sub(ctx, owner, repo, event, callback, secret)
}

func (s repositoryHooksStub) Unsubscribe(ctx context.Context, owner, repo, event, callback string, secret []byte) (*github.Response, error) {
	return s.unsub(ctx, owner, repo, event, callback, secret)
}

func TestRepositoryHooksRoundTripThroughGoGitHubClient(t *testing.T) {
	t.Parallel()
	var calls []string
	stub := repositoryHooksStub{
		create: func(_ context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error) {
			calls = append(calls, "create")
			assert.Equal(t, "octo", owner)
			assert.Equal(t, "demo", repo)
			assert.Equal(t, []string{"push"}, hook.Events)
			assert.Equal(t, "https://example.test/hook", hook.Config.GetURL())
			return &github.Hook{ID: github.Ptr(int64(41)), Events: hook.Events}, response(http.StatusCreated), nil
		},
		list: func(_ context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
			calls = append(calls, "list")
			assert.Equal(t, "octo", owner)
			assert.Equal(t, "demo", repo)
			assert.Equal(t, 2, opts.Page)
			assert.Equal(t, 50, opts.PerPage)
			return []*github.Hook{{ID: github.Ptr(int64(41))}}, response(http.StatusOK), nil
		},
		get: func(_ context.Context, owner, repo string, id int64) (*github.Hook, *github.Response, error) {
			calls = append(calls, "get")
			assert.Equal(t, "octo", owner)
			assert.Equal(t, "demo", repo)
			assert.Equal(t, int64(41), id)
			return &github.Hook{ID: &id}, response(http.StatusOK), nil
		},
		edit: func(_ context.Context, owner, repo string, id int64, body *github.Hook) (*github.Hook, *github.Response, error) {
			calls = append(calls, "edit")
			assert.Equal(t, "octo", owner)
			assert.Equal(t, "demo", repo)
			assert.Equal(t, int64(41), id)
			assert.Equal(t, []string{"issues"}, body.Events)
			return &github.Hook{ID: &id, Events: body.Events}, response(http.StatusOK), nil
		},
		delete: func(_ context.Context, owner, repo string, id int64) (*github.Response, error) {
			calls = append(calls, "delete")
			assert.Equal(t, int64(41), id)
			return response(http.StatusNoContent), nil
		},
	}

	server := httptest.NewServer(New(Services{Repositories: stub}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	created, createdResponse, err := client.Repositories.CreateHook(t.Context(), "octo", "demo", &github.Hook{
		Config: &github.HookConfig{URL: github.Ptr("https://example.test/hook")},
		Events: []string{"push"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(41), created.GetID())
	assert.Equal(t, http.StatusCreated, createdResponse.StatusCode)

	listed, _, err := client.Repositories.ListHooks(t.Context(), "octo", "demo", &github.ListOptions{Page: 2, PerPage: 50})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, int64(41), listed[0].GetID())

	got, _, err := client.Repositories.GetHook(t.Context(), "octo", "demo", 41)
	require.NoError(t, err)
	assert.Equal(t, int64(41), got.GetID())

	edited, _, err := client.Repositories.EditHook(t.Context(), "octo", "demo", 41, &github.Hook{Events: []string{"issues"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"issues"}, edited.Events)

	deletedResponse, err := client.Repositories.DeleteHook(t.Context(), "octo", "demo", 41)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, deletedResponse.StatusCode)
	assert.Equal(t, []string{"create", "list", "get", "edit", "delete"}, calls)
}

type authContextKey struct{}

type recordingAuthenticator struct {
	mu    sync.Mutex
	calls []string
}

func (a *recordingAuthenticator) AuthenticateWithPAT(ctx context.Context, token string) (context.Context, error) {
	a.mu.Lock()
	a.calls = append(a.calls, "pat:"+token)
	a.mu.Unlock()
	return context.WithValue(ctx, authContextKey{}, "pat"), nil
}

func (a *recordingAuthenticator) AuthenticateWithInstallationToken(ctx context.Context, token string) (context.Context, error) {
	a.mu.Lock()
	a.calls = append(a.calls, "installation:"+token)
	a.mu.Unlock()
	return context.WithValue(ctx, authContextKey{}, "installation"), nil
}

func TestAuthenticationDelegatesPATAndInstallationTokens(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthenticator{}
	stub := repositoryHooksStub{
		get: func(ctx context.Context, _, _ string, id int64) (*github.Hook, *github.Response, error) {
			assert.Contains(t, []any{"pat", "installation"}, ctx.Value(authContextKey{}))
			return &github.Hook{ID: &id}, response(http.StatusOK), nil
		},
	}
	server := httptest.NewServer(New(Services{Repositories: stub}, auth))
	t.Cleanup(server.Close)

	for _, token := range []string{"ghp_personal-opaque", "github_pat_fine-grained-opaque", "ghs_installation-opaque"} {
		client := newGitHubClient(t, server.URL, token)
		_, _, err := client.Repositories.GetHook(t.Context(), "octo", "demo", 1)
		require.NoError(t, err)
	}

	auth.mu.Lock()
	defer auth.mu.Unlock()
	assert.Equal(t, []string{
		"pat:ghp_personal-opaque",
		"pat:github_pat_fine-grained-opaque",
		"installation:ghs_installation-opaque",
	}, auth.calls)
}

func TestHubCollisionDispatchesByFormMode(t *testing.T) {
	t.Parallel()
	var calls []string
	verify := func(mode string) func(context.Context, string, string, string, string, []byte) (*github.Response, error) {
		return func(_ context.Context, owner, repo, event, callback string, secret []byte) (*github.Response, error) {
			calls = append(calls, mode)
			assert.Equal(t, "octo", owner)
			assert.Equal(t, "demo", repo)
			assert.Equal(t, "push", event)
			assert.Equal(t, "https://example.test/callback", callback)
			assert.Equal(t, []byte("opaque-secret"), secret)
			return response(http.StatusNoContent), nil
		}
	}
	stub := repositoryHooksStub{sub: verify("subscribe"), unsub: verify("unsubscribe")}
	server := httptest.NewServer(New(Services{Repositories: stub}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	_, err := client.Repositories.Subscribe(t.Context(), "octo", "demo", "push", "https://example.test/callback", []byte("opaque-secret"))
	require.NoError(t, err)
	_, err = client.Repositories.Unsubscribe(t.Context(), "octo", "demo", "push", "https://example.test/callback", []byte("opaque-secret"))
	require.NoError(t, err)
	assert.Equal(t, []string{"subscribe", "unsubscribe"}, calls)
}

type contentsStub struct {
	UnimplementedRepositoriesService
}

func (contentsStub) GetContents(_ context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
	content := base64.StdEncoding.EncodeToString([]byte("hello from the server"))
	return &github.RepositoryContent{
		Type:     github.Ptr("file"),
		Path:     &path,
		Encoding: github.Ptr("base64"),
		Content:  &content,
	}, nil, response(http.StatusOK), nil
}

func TestSharedContentsRouteReachesWireMethod(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{Repositories: contentsStub{}}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	file, directory, _, err := client.Repositories.GetContents(t.Context(), "octo", "demo", "docs/readme.md", &github.RepositoryContentGetOptions{Ref: "main"})
	require.NoError(t, err)
	require.NotNil(t, file)
	assert.Nil(t, directory)
	assert.Equal(t, "docs/readme.md", file.GetPath())
	decoded, err := file.GetContent()
	require.NoError(t, err)
	assert.Equal(t, "hello from the server", decoded)

	reader, _, err := client.Repositories.DownloadContents(t.Context(), "octo", "demo", "docs/readme.md", nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	payload, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "hello from the server", string(payload))
}

type releaseAssetsStub struct {
	UnimplementedRepositoriesService
}

func (releaseAssetsStub) GetReleaseAsset(_ context.Context, owner, repo string, id int64) (*github.ReleaseAsset, *github.Response, error) {
	return &github.ReleaseAsset{ID: &id, Name: github.Ptr("artifact.zip")}, response(http.StatusOK), nil
}

func (releaseAssetsStub) DownloadReleaseAsset(_ context.Context, owner, repo string, id int64, client *http.Client) (io.ReadCloser, string, error) {
	return nil, "https://downloads.example.test/artifact.zip", nil
}

func (releaseAssetsStub) UploadReleaseAsset(_ context.Context, owner, repo string, id int64, opts *github.UploadOptions, file *os.File) (*github.ReleaseAsset, *github.Response, error) {
	payload, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	if string(payload) != "binary payload" {
		return nil, nil, assert.AnError
	}
	return &github.ReleaseAsset{ID: github.Ptr(int64(99)), Name: &opts.Name}, response(http.StatusCreated), nil
}

func TestReleaseAssetCollisionUsesAcceptAndUploadBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{Repositories: releaseAssetsStub{}}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	asset, _, err := client.Repositories.GetReleaseAsset(t.Context(), "octo", "demo", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(42), asset.GetID())

	reader, location, err := client.Repositories.DownloadReleaseAsset(t.Context(), "octo", "demo", 42, nil)
	require.NoError(t, err)
	assert.Nil(t, reader)
	assert.Equal(t, "https://downloads.example.test/artifact.zip", location)

	file, err := os.CreateTemp(t.TempDir(), "asset-*.zip")
	require.NoError(t, err)
	_, err = file.WriteString("binary payload")
	require.NoError(t, err)
	_, err = file.Seek(0, io.SeekStart)
	require.NoError(t, err)
	uploaded, _, err := client.Repositories.UploadReleaseAsset(t.Context(), "octo", "demo", 42, &github.UploadOptions{Name: "artifact.zip", MediaType: "application/zip"}, file)
	require.NoError(t, err)
	assert.Equal(t, int64(99), uploaded.GetID())
}

type organizationPermissionsStub struct {
	UnimplementedOrganizationsService
}

func (organizationPermissionsStub) GetActionsPermissions(_ context.Context, org string) (*github.ActionsPermissions, *github.Response, error) {
	return &github.ActionsPermissions{EnabledRepositories: github.Ptr("selected")}, response(http.StatusOK), nil
}

func TestCrossServiceAliasFallsThroughToImplementedService(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{
		Actions:       UnimplementedActionsService{},
		Organizations: organizationPermissionsStub{},
	}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	permissions, _, err := client.Actions.GetActionsPermissions(t.Context(), "octo")
	require.NoError(t, err)
	assert.Equal(t, "selected", permissions.GetEnabledRepositories())
}

type actionsVariableStub struct {
	UnimplementedActionsService
}

func (actionsVariableStub) AddSelectedRepoToOrgVariable(_ context.Context, org, name string, repo *github.Repository) (*github.Response, error) {
	if org != "octo" || name != "deploy-target" || repo.GetID() != 123 {
		return nil, assert.AnError
	}
	return response(http.StatusNoContent), nil
}

func (actionsVariableStub) SetSelectedReposForOrgVariable(_ context.Context, org, name string, ids []int64) (*github.Response, error) {
	if org != "octo" || name != "deploy-target" || !assert.ObjectsAreEqual([]int64{123, 456}, ids) {
		return nil, assert.AnError
	}
	return response(http.StatusNoContent), nil
}

func (actionsVariableStub) CreateWorkflowDispatchEventByID(_ context.Context, owner, repo string, workflowID int64, body github.CreateWorkflowDispatchEventRequest) (*github.WorkflowDispatchRunDetails, *github.Response, error) {
	if owner != "octo" || repo != "demo" || workflowID != 77 || body.Ref != "main" {
		return nil, nil, assert.AnError
	}
	return &github.WorkflowDispatchRunDetails{WorkflowRunID: github.Ptr(int64(88))}, response(http.StatusCreated), nil
}

func TestPathParameterRehydratesEntityField(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{Actions: actionsVariableStub{}}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	result, err := client.Actions.AddSelectedRepoToOrgVariable(t.Context(), "octo", "deploy-target", &github.Repository{ID: github.Ptr(int64(123))})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, result.StatusCode)
	result, err = client.Actions.SetSelectedReposForOrgVariable(t.Context(), "octo", "deploy-target", []int64{123, 456})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, result.StatusCode)
	details, _, err := client.Actions.CreateWorkflowDispatchEventByID(t.Context(), "octo", "demo", 77, github.CreateWorkflowDispatchEventRequest{Ref: "main", ReturnRunDetails: github.Ptr(true)})
	require.NoError(t, err)
	assert.Equal(t, int64(88), details.GetWorkflowRunID())
}

type repositoryCommitStub struct {
	UnimplementedRepositoriesService
}

func (repositoryCommitStub) GetCommit(_ context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
	return &github.RepositoryCommit{SHA: &sha}, response(http.StatusOK), nil
}

func (repositoryCommitStub) GetCommitRaw(_ context.Context, owner, repo, sha string, opts github.RawOptions) (string, *github.Response, error) {
	kind := map[github.RawType]string{github.Diff: "diff", github.Patch: "patch"}[opts.Type]
	return kind + " for " + sha, response(http.StatusOK), nil
}

func (repositoryCommitStub) GetCommitSHA1(_ context.Context, owner, repo, ref, lastSHA string) (string, *github.Response, error) {
	return "deadbeef", response(http.StatusOK), nil
}

func TestAcceptDisambiguatesJSONDiffPatchAndSHA(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{Repositories: repositoryCommitStub{}}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	commit, _, err := client.Repositories.GetCommit(t.Context(), "octo", "demo", "main", nil)
	require.NoError(t, err)
	assert.Equal(t, "main", commit.GetSHA())

	diff, _, err := client.Repositories.GetCommitRaw(t.Context(), "octo", "demo", "main", github.RawOptions{Type: github.Diff})
	require.NoError(t, err)
	assert.Equal(t, "diff for main", diff)
	patch, _, err := client.Repositories.GetCommitRaw(t.Context(), "octo", "demo", "main", github.RawOptions{Type: github.Patch})
	require.NoError(t, err)
	assert.Equal(t, "patch for main", patch)
	sha, _, err := client.Repositories.GetCommitSHA1(t.Context(), "octo", "demo", "main", "")
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", sha)
}

type redirectActionsStub struct {
	UnimplementedActionsService
}

func (redirectActionsStub) DownloadArtifact(_ context.Context, owner, repo string, artifactID int64, maxRedirects int) (*url.URL, *github.Response, error) {
	location, err := url.Parse("https://downloads.example.test/artifacts/5.zip")
	return location, response(http.StatusFound), err
}

func (redirectActionsStub) GetWorkflowRunLogs(_ context.Context, owner, repo string, runID int64, maxRedirects int) (*url.URL, *github.Response, error) {
	location, err := url.Parse("https://downloads.example.test/runs/9.zip")
	return location, response(http.StatusFound), err
}

type redirectMigrationStub struct {
	UnimplementedMigrationService
}

func (redirectMigrationStub) MigrationArchiveURL(_ context.Context, org string, id int64) (string, error) {
	return "https://downloads.example.test/migrations/7.tar.gz", nil
}

type archiveAndCompareStub struct {
	UnimplementedRepositoriesService
}

func (archiveAndCompareStub) GetArchiveLink(_ context.Context, owner, repo string, format github.ArchiveFormat, opts *github.RepositoryContentGetOptions, maxRedirects int) (*url.URL, *github.Response, error) {
	location, err := url.Parse("https://downloads.example.test/" + string(format) + "/" + opts.Ref)
	return location, response(http.StatusFound), err
}

func (archiveAndCompareStub) CompareCommits(_ context.Context, owner, repo, base, head string, opts *github.ListOptions) (*github.CommitsComparison, *github.Response, error) {
	return &github.CommitsComparison{Status: github.Ptr(base + "..." + head)}, response(http.StatusOK), nil
}

func TestRedirectFamiliesAndCompositeRefsRoundTrip(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{
		Actions:      redirectActionsStub{},
		Migration:    redirectMigrationStub{},
		Repositories: archiveAndCompareStub{},
	}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	artifact, _, err := client.Actions.DownloadArtifact(t.Context(), "octo", "demo", 5, 0)
	require.NoError(t, err)
	assert.Equal(t, "https://downloads.example.test/artifacts/5.zip", artifact.String())
	runLogs, _, err := client.Actions.GetWorkflowRunLogs(t.Context(), "octo", "demo", 9, 0)
	require.NoError(t, err)
	assert.Equal(t, "https://downloads.example.test/runs/9.zip", runLogs.String())
	migration, err := client.Migrations.MigrationArchiveURL(t.Context(), "octo", 7)
	require.NoError(t, err)
	assert.Equal(t, "https://downloads.example.test/migrations/7.tar.gz", migration)
	archive, _, err := client.Repositories.GetArchiveLink(t.Context(), "octo", "demo", github.Zipball, &github.RepositoryContentGetOptions{Ref: "main"}, 0)
	require.NoError(t, err)
	assert.Equal(t, "https://downloads.example.test/zipball/main", archive.String())
	comparison, _, err := client.Repositories.CompareCommits(t.Context(), "octo", "demo", "v1", "v2", nil)
	require.NoError(t, err)
	assert.Equal(t, "v1...v2", comparison.GetStatus())
}

type activitySubscriptionStub struct {
	UnimplementedActivityService
}

func (activitySubscriptionStub) GetRepositorySubscription(_ context.Context, owner, repo string) (*github.Subscription, *github.Response, error) {
	return &github.Subscription{Subscribed: github.Ptr(true)}, response(http.StatusOK), nil
}

type issueImportStub struct {
	UnimplementedIssueImportService
}

func (issueImportStub) CheckStatusSince(_ context.Context, owner, repo string, since github.Timestamp) ([]*github.IssueImportResponse, *github.Response, error) {
	if since.Format(time.DateOnly) != "2026-07-01" {
		return nil, nil, assert.AnError
	}
	return []*github.IssueImportResponse{}, response(http.StatusOK), nil
}

type remainingMigrationStub struct {
	UnimplementedMigrationService
}

func (remainingMigrationStub) UserMigrationArchiveURL(_ context.Context, id int64) (string, error) {
	return "https://downloads.example.test/user-migrations/" + strconv.FormatInt(id, 10), nil
}

type rulesetStub struct {
	UnimplementedRepositoriesService
}

func (rulesetStub) GetRuleset(_ context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
	if !includesParents {
		return nil, nil, assert.AnError
	}
	return &github.RepositoryRuleset{ID: &rulesetID}, response(http.StatusOK), nil
}

func TestSignatureKindsAndFormattedQueryValuesRoundTrip(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(Services{
		Activity:     activitySubscriptionStub{},
		IssueImport:  issueImportStub{},
		Migration:    remainingMigrationStub{},
		Repositories: rulesetStub{},
	}, nil))
	t.Cleanup(server.Close)
	client := newGitHubClient(t, server.URL, "")

	subscription, _, err := client.Activity.GetRepositorySubscription(t.Context(), "octo", "demo")
	require.NoError(t, err)
	assert.True(t, subscription.GetSubscribed())
	imports, _, err := client.IssueImport.CheckStatusSince(t.Context(), "octo", "demo", github.Timestamp{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Empty(t, imports)
	migration, err := client.Migrations.UserMigrationArchiveURL(t.Context(), 8)
	require.NoError(t, err)
	assert.Equal(t, "https://downloads.example.test/user-migrations/8", migration)
	ruleset, _, err := client.Repositories.GetRuleset(t.Context(), "octo", "demo", 12, true)
	require.NoError(t, err)
	assert.Equal(t, int64(12), ruleset.GetID())
}

func TestAuthenticationRejectsUnsupportedForms(t *testing.T) {
	t.Parallel()
	auth := &recordingAuthenticator{}
	server := httptest.NewServer(New(Services{Repositories: repositoryHooksStub{}}, auth))
	t.Cleanup(server.Close)

	for _, header := range []string{"Basic dXNlcjpwYXNz", "Bearer gho_oauth", "Bearer opaque"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/repos/octo/demo/hooks/1", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", header)
		result, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.NoError(t, result.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
	}
	assert.Empty(t, auth.calls)
}

func TestCompleteGeneratedSurfaceConstructsAndDispatches(t *testing.T) {
	t.Parallel()
	mux := New(generatedUnimplementedServices(), nil)
	request := httptest.NewRequest(http.MethodGet, "/repos/octo/demo/hooks/1", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	assert.Equal(t, http.StatusNotImplemented, response.Code)
}

func TestServiceErrorResponseRoundTripsThroughGoGitHubClient(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusUnprocessableEntity,
	} {
		status := status
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			stub := repositoryHooksStub{
				get: func(context.Context, string, string, int64) (*github.Hook, *github.Response, error) {
					return nil, nil, &github.ErrorResponse{
						Response: &http.Response{
							StatusCode: status,
							Header: http.Header{
								"X-GitHub-Request-Id": []string{"request-123"},
							},
						},
						Message:          http.StatusText(status),
						Errors:           []github.Error{{Resource: "Hook", Field: "id", Code: "invalid"}},
						DocumentationURL: "https://docs.github.test/errors",
					}
				},
			}
			server := httptest.NewServer(New(Services{Repositories: stub}, nil))
			t.Cleanup(server.Close)
			client := newGitHubClient(t, server.URL, "")

			_, response, err := client.Repositories.GetHook(t.Context(), "octo", "demo", 1)
			require.Error(t, err)
			var errorResponse *github.ErrorResponse
			require.ErrorAs(t, err, &errorResponse)
			require.NotNil(t, response)
			assert.Equal(t, status, response.StatusCode)
			assert.Equal(t, "request-123", response.Header.Get("X-GitHub-Request-Id"))
			assert.Equal(t, http.StatusText(status), errorResponse.Message)
			assert.Equal(t, []github.Error{{Resource: "Hook", Field: "id", Code: "invalid"}}, errorResponse.Errors)
			assert.Equal(t, "https://docs.github.test/errors", errorResponse.DocumentationURL)
		})
	}
}

func TestUnexpectedServiceErrorRemainsInternalServerError(t *testing.T) {
	t.Parallel()
	stub := repositoryHooksStub{
		get: func(context.Context, string, string, int64) (*github.Hook, *github.Response, error) {
			return nil, nil, errors.New("database unavailable")
		},
	}
	server := httptest.NewServer(New(Services{Repositories: stub}, nil))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/repos/octo/demo/hooks/1", nil)
	require.NoError(t, err)
	result, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, result.Body.Close()) })

	assert.Equal(t, http.StatusInternalServerError, result.StatusCode)
}

func TestEveryGeneratedRouteHasAnUnambiguousRepresentativePath(t *testing.T) {
	t.Parallel()
	routes := buildRouteGroups(generatedOperations(generatedUnimplementedServices()))
	wildcard := regexp.MustCompile(`\{p\d+(?:\.\.\.)?\}`)
	for _, expected := range routes {
		op := expected.operations[0]
		path := wildcard.ReplaceAllString(op.Pattern, "123")
		request := httptest.NewRequest(op.HTTPMethod, path, nil)
		var matched *routeGroup
		for index := range routes {
			if _, ok := routes[index].match(request); ok {
				matched = &routes[index]
				break
			}
		}
		require.NotNilf(t, matched, "%s %s did not match", op.HTTPMethod, op.Pattern)
		assert.Equalf(t, op.HTTPMethod+" "+op.Pattern, matched.operations[0].HTTPMethod+" "+matched.operations[0].Pattern, "representative path %s selected the wrong route", path)
	}
}

func response(status int) *github.Response {
	return &github.Response{Response: &http.Response{StatusCode: status, Header: make(http.Header)}}
}

func newGitHubClient(t *testing.T, baseURL, token string) *github.Client {
	t.Helper()
	options := []github.ClientOptionsFunc{github.WithEnterpriseURLs(baseURL+"/", baseURL+"/")}
	if token != "" {
		options = append(options, github.WithAuthToken(token))
	}
	client, err := github.NewClient(options...)
	require.NoError(t, err)
	return client
}
