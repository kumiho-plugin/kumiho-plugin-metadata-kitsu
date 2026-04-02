package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kumiho-plugin/kumiho-plugin-sdk/capability"
	sdkconfig "github.com/kumiho-plugin/kumiho-plugin-sdk/config"
	pluginerrors "github.com/kumiho-plugin/kumiho-plugin-sdk/errors"
	"github.com/kumiho-plugin/kumiho-plugin-sdk/healthcheck"
	"github.com/kumiho-plugin/kumiho-plugin-sdk/manifest"
	sdktypes "github.com/kumiho-plugin/kumiho-plugin-sdk/types"
	"github.com/kumiho-plugin/kumiho-plugin-sdk/version"
)

const (
	providerName         = "kitsu"
	baseURL              = "https://kitsu.io/api/edge"
	algoliaAppID         = "AWQO5J657S"
	pluginID             = "kumiho-plugin-metadata-kitsu"
	pluginVer            = "0.1.0"
	tokenURL             = "https://kitsu.io/api/oauth/token"
	maxFetchedCharacters = 20

	// clientID, clientSecret는 의도적으로 빈 값으로 둔다.
	// Kitsu OAuth에서 실제 client_id/client_secret를 넣으면 Algolia 공개 검색 티어로
	// 라우팅되어, 서버 운영자 전체가 동일 공개 키로 접속해 트래픽 문제가 생길 수 있다.
	// 빈 값으로 두면 Kitsu edge search 경로(/api/edge/manga)로 동작하며,
	// Kitsu 공식 문서에서도 비인증 접근을 허용하는 방식이다.
	clientID     = ""
	clientSecret = ""
	requestGap   = 300 * time.Millisecond
)

type Plugin struct {
	baseURL        string
	algoliaBaseURL string
	client         *http.Client
	accessToken    string
	refreshToken   string
	rateMu         sync.Mutex
	nextRequestAt  time.Time
}

type searchResponse struct {
	Data []mangaResource `json:"data"`
}

type fetchResponse struct {
	Data     mangaResource      `json:"data"`
	Included []includedResource `json:"included"`
}

type includedResource struct {
	ID            string                `json:"id"`
	Type          string                `json:"type"`
	Attributes    includedAttributes    `json:"attributes"`
	Relationships includedRelationships `json:"relationships"`
}

type includedAttributes struct {
	Title         string   `json:"title"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	CanonicalName string   `json:"canonicalName"`
	Image         imageSet `json:"image"`
	Role          string   `json:"role"`
}

type includedRelationships struct {
	Character relatedResource `json:"character"`
}

type relatedResource struct {
	Data resourceIdentifier `json:"data"`
}

type resourceIdentifier struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type algoliaKeyResponse struct {
	Media algoliaIndexKey `json:"media"`
}

type algoliaIndexKey struct {
	Key   string `json:"key"`
	Index string `json:"index"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

type algoliaSearchRequest struct {
	Query       string `json:"query"`
	Filters     string `json:"filters,omitempty"`
	HitsPerPage int    `json:"hitsPerPage,omitempty"`
}

type algoliaSearchResponse struct {
	Hits []algoliaMediaHit `json:"hits"`
}

type algoliaMediaHit struct {
	ID                int               `json:"id"`
	Kind              string            `json:"kind"`
	Slug              string            `json:"slug"`
	Titles            map[string]string `json:"titles"`
	AbbreviatedTitles []string          `json:"abbreviatedTitles"`
	CanonicalTitle    string            `json:"canonicalTitle"`
	Description       map[string]string `json:"description"`
	Synopsis          string            `json:"synopsis"`
	AgeRating         string            `json:"ageRating"`
	Subtype           string            `json:"subtype"`
	PosterImage       imageSet          `json:"posterImage"`
	StartDateUnix     int64             `json:"startDate"`
	Year              int               `json:"year"`
	ChapterCount      *int              `json:"chapterCount"`
	VolumeCount       *int              `json:"volumeCount"`
}

type mangaResource struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Attributes mangaAttributes `json:"attributes"`
}

type mangaAttributes struct {
	Slug              string            `json:"slug"`
	Synopsis          string            `json:"synopsis"`
	Description       string            `json:"description"`
	Titles            map[string]string `json:"titles"`
	CanonicalTitle    string            `json:"canonicalTitle"`
	AbbreviatedTitles []string          `json:"abbreviatedTitles"`
	StartDate         string            `json:"startDate"`
	EndDate           string            `json:"endDate"`
	AgeRating         string            `json:"ageRating"`
	AgeRatingGuide    string            `json:"ageRatingGuide"`
	Subtype           string            `json:"subtype"`
	Status            string            `json:"status"`
	PosterImage       imageSet          `json:"posterImage"`
	ChapterCount      *int              `json:"chapterCount"`
	VolumeCount       *int              `json:"volumeCount"`
	Serialization     string            `json:"serialization"`
	MangaType         string            `json:"mangaType"`
}

type imageSet struct {
	Tiny     string `json:"tiny"`
	Small    string `json:"small"`
	Medium   string `json:"medium"`
	Large    string `json:"large"`
	Original string `json:"original"`
}

func New(base string, accessToken string, refreshToken string, client *http.Client) *Plugin {
	if client == nil {
		client = http.DefaultClient
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = baseURL
	}
	return &Plugin{
		baseURL:        strings.TrimRight(base, "/"),
		algoliaBaseURL: "https://" + algoliaAppID + "-dsn.algolia.net",
		client:         client,
		accessToken:    strings.TrimSpace(accessToken),
		refreshToken:   strings.TrimSpace(refreshToken),
	}
}

func (p *Plugin) Search(ctx context.Context, req *sdktypes.SearchRequest) (*sdktypes.SearchResponse, error) {
	if pluginErr := validateContentType(req); pluginErr != nil {
		return &sdktypes.SearchResponse{Error: pluginErr}, nil
	}

	query := buildSearchQuery(req)
	if query == "" {
		return &sdktypes.SearchResponse{
			Error: pluginerrors.New(pluginerrors.ErrCodeInvalidRequest, "search query requires title, series, or filename"),
		}, nil
	}

	params := url.Values{}
	limit := limitOrDefault(req.Limit, 5, 10)

	if p.accessToken != "" {
		if candidates, pluginErr := p.searchWithAlgolia(ctx, req, query, limit); pluginErr == nil && len(candidates) > 0 {
			return &sdktypes.SearchResponse{Candidates: candidates}, nil
		}
	}

	params.Set("filter[text]", query)
	params.Set("page[limit]", strconv.Itoa(limit))

	var payload searchResponse
	if pluginErr := p.getJSON(ctx, p.baseURL+"/manga?"+params.Encode(), &payload); pluginErr != nil {
		return &sdktypes.SearchResponse{Error: pluginErr}, nil
	}
	candidates := p.mapEdgeCandidates(req, payload.Data)
	return &sdktypes.SearchResponse{Candidates: candidates}, nil
}

func (p *Plugin) Fetch(ctx context.Context, req *sdktypes.FetchRequest) (*sdktypes.FetchResponse, error) {
	if req == nil || strings.TrimSpace(req.Source.ID) == "" {
		return &sdktypes.FetchResponse{
			Error: pluginerrors.New(pluginerrors.ErrCodeInvalidRequest, "source.id is required"),
		}, nil
	}

	var payload fetchResponse
	endpoint := p.baseURL + "/manga/" + url.PathEscape(strings.TrimSpace(req.Source.ID)) + "?include=categories,characters,characters.character"
	if pluginErr := p.getJSON(ctx, endpoint, &payload); pluginErr != nil {
		return &sdktypes.FetchResponse{Error: pluginErr}, nil
	}
	if !isSupportedSubtype(payload.Data.Attributes.Subtype) {
		return &sdktypes.FetchResponse{
			Error: pluginerrors.New(pluginerrors.ErrCodeUnsupported, fmt.Sprintf("kitsu subtype %q is not supported", payload.Data.Attributes.Subtype)),
		}, nil
	}
	contentType := contentTypeForSubtype(payload.Data.Attributes.Subtype)
	if contentType == "" {
		contentType = sdktypes.ContentTypeComic
	}

	result := &sdktypes.MetadataResult{
		Source: sdktypes.SourceRef{
			ID:   payload.Data.ID,
			Name: providerName,
			URL:  resourceURL(payload.Data.Attributes.Slug, payload.Data.ID),
		},
		Title:           chooseFetchTitle(payload.Data.Attributes),
		OriginalTitle:   chooseOriginalTitle(payload.Data.Attributes),
		OriginalTitles:  originalTitles(payload.Data.Attributes.Titles),
		Description:     strings.TrimSpace(firstNonEmpty(payload.Data.Attributes.Description, payload.Data.Attributes.Synopsis)),
		ContentType:     contentType,
		PublicationDate: strings.TrimSpace(payload.Data.Attributes.StartDate),
		Identifiers:     mapIdentifiers(payload.Data),
		Cover:           coverFrom(payload.Data.Attributes.PosterImage),
	}

	if lang := languageFromTitles(payload.Data.Attributes.Titles); lang != "" {
		result.Language = sdktypes.Language(lang)
	}
	result.Tags = buildFetchTags(payload)
	result.Characters = buildFetchCharacters(payload)

	return &sdktypes.FetchResponse{Result: result}, nil
}

func (p *Plugin) Healthcheck(context.Context) (*healthcheck.Response, error) {
	message := "kitsu manga plugin ready (edge search + edge fetch)"
	if p.accessToken != "" {
		message = "kitsu manga plugin ready (user-scoped algolia search + edge fetch)"
	}
	return &healthcheck.Response{
		Status:  healthcheck.StatusOK,
		Version: pluginVer,
		Message: message,
	}, nil
}

func (p *Plugin) Manifest() *manifest.Manifest {
	return &manifest.Manifest{
		ID:                  pluginID,
		Name:                "Kitsu Manga",
		Description:         "Kitsu manga metadata provider for Kumiho",
		DescriptionI18n:     localized("manifest.description", "Kitsu manga metadata provider for Kumiho"),
		Version:             pluginVer,
		Author:              "aha-hyeong",
		License:             "Apache-2.0",
		Homepage:            "https://kitsu.app/",
		Repository:          "https://github.com/kumiho-plugin/kumiho-plugin-metadata-kitsu",
		TrustLevel:          manifest.TrustLevelOfficial,
		RuntimeType:         manifest.RuntimeTypeService,
		SupportedPlatforms:  []manifest.Platform{manifest.PlatformLinuxDocker},
		Capabilities:        []capability.Capability{capability.MetadataSearch, capability.MetadataFetch},
		Permissions:         []string{"network"},
		MinCoreVersion:      "0.1.0",
		ConfigSchemaVersion: "1",
		ConfigSchema: &sdkconfig.Schema{
			Version: "1",
			Fields: []sdkconfig.ConfigField{
				{
					Key:             "access_token",
					Type:            sdkconfig.FieldTypeSecret,
					Label:           "Access Token",
					LabelI18n:       localized("config.access_token.label", "Access Token"),
					Required:        false,
					EnvKey:          "KITSU_ACCESS_TOKEN",
					Description:     "Optional token used for user-scoped advanced search.",
					DescriptionI18n: localized("config.access_token.description", "Optional token used for user-scoped advanced search."),
				},
				{
					Key:             "refresh_token",
					Type:            sdkconfig.FieldTypeSecret,
					Label:           "Refresh Token",
					LabelI18n:       localized("config.refresh_token.label", "Refresh Token"),
					Required:        false,
					EnvKey:          "KITSU_REFRESH_TOKEN",
					Description:     "Optional refresh token used to renew the access token.",
					DescriptionI18n: localized("config.refresh_token.description", "Optional refresh token used to renew the access token."),
				},
			},
		},
		Auth: &sdkconfig.AuthSchema{
			Actions: []sdkconfig.AuthAction{
				{
					ID:                           "login",
					Type:                         sdkconfig.AuthActionTypePasswordGrant,
					Title:                        "Kitsu Login",
					TitleI18n:                    localized("auth.login.title", "Kitsu Login"),
					Description:                  "Log in with a Kitsu account to issue and store tokens. The password is not stored.",
					DescriptionI18n:              localized("auth.login.description", "Log in with a Kitsu account to issue and store tokens. The password is not stored."),
					ButtonLabel:                  "Kitsu Login",
					ButtonLabelI18n:              localized("auth.login.button", "Kitsu Login"),
					RepeatLabel:                  "Kitsu Re-login",
					RepeatLabelI18n:              localized("auth.login.repeat", "Re-login to Kitsu"),
					DeleteLabel:                  "Remove Tokens",
					DeleteLabelI18n:              localized("auth.login.delete", "Remove Tokens"),
					RequiredMessage:              "Enter the required credentials first.",
					RequiredMessageI18n:          localized("auth.login.required", "Enter your Kitsu email (or slug) and password first."),
					SuccessMessage:               "Kitsu tokens saved.",
					SuccessMessageI18n:           localized("auth.login.success", "Kitsu tokens saved."),
					SuccessReactivateMessage:     "Kitsu tokens saved. Activate the plugin again to apply them.",
					SuccessReactivateMessageI18n: localized("auth.login.success_reactivate", "Kitsu tokens saved. Activate the plugin again to apply them."),
					ErrorMessage:                 "Failed to log in to Kitsu.",
					ErrorMessageI18n:             localized("auth.login.error", "Failed to log in to Kitsu."),
					ErrorMessages: map[string]string{
						"invalid_request": "Enter your Kitsu email (or slug) and password before logging in.",
						"unauthorized":    "Your Kitsu email, slug, or password is incorrect.",
						"timeout":         "Kitsu did not respond in time. Try again in a moment.",
						"rate_limited":    "Kitsu temporarily limited login attempts. Please wait and try again.",
						"provider_error":  "Kitsu could not issue a token right now. Try again later.",
					},
					ErrorMessagesI18n: map[string]sdkconfig.LocalizedString{
						"invalid_request": localized("auth.login.errors.invalid_request", "Enter your Kitsu email (or slug) and password before logging in."),
						"unauthorized":    localized("auth.login.errors.unauthorized", "Your Kitsu email, slug, or password is incorrect."),
						"timeout":         localized("auth.login.errors.timeout", "Kitsu did not respond in time. Try again in a moment."),
						"rate_limited":    localized("auth.login.errors.rate_limited", "Kitsu temporarily limited login attempts. Please wait and try again."),
						"provider_error":  localized("auth.login.errors.provider_error", "Kitsu could not issue a token right now. Try again later."),
					},
					DeleteMessage:               "Stored Kitsu tokens removed.",
					DeleteMessageI18n:           localized("auth.login.delete_success", "Stored Kitsu tokens removed."),
					DeleteReactivateMessage:     "Stored Kitsu tokens removed. Activate the plugin again to use the default search.",
					DeleteReactivateMessageI18n: localized("auth.login.delete_reactivate", "Stored Kitsu tokens removed. Activate the plugin again to use the default search."),
					DeleteErrorMessage:          "Failed to remove the stored Kitsu tokens.",
					DeleteErrorMessageI18n:      localized("auth.login.delete_error", "Failed to remove the stored Kitsu tokens."),
					Endpoint:                    tokenURL,
					Params: map[string]string{
						"grant_type":    "password",
						"client_id":     clientID,
						"client_secret": clientSecret,
					},
					Fields: []sdkconfig.ConfigField{
						{
							Key:             "username",
							Type:            sdkconfig.FieldTypeString,
							Label:           "Email or slug",
							LabelI18n:       localized("auth.login.field.username.label", "Email or slug"),
							Required:        true,
							Placeholder:     "Kitsu email or username",
							PlaceholderI18n: localized("auth.login.field.username.placeholder", "Kitsu email or username"),
							AutoComplete:    "username",
						},
						{
							Key:             "password",
							Type:            sdkconfig.FieldTypeSecret,
							Label:           "Password",
							LabelI18n:       localized("auth.login.field.password.label", "Password"),
							Required:        true,
							Placeholder:     "Kitsu password",
							PlaceholderI18n: localized("auth.login.field.password.placeholder", "Kitsu password"),
							AutoComplete:    "current-password",
						},
					},
					StoreMappings: map[string]string{
						"access_token":  "access_token",
						"refresh_token": "refresh_token",
					},
				},
			},
		},
		SDKVersion: version.SDK,
	}
}

func (p *Plugin) getJSON(ctx context.Context, endpoint string, out any) *pluginerrors.PluginError {
	return p.getJSONWithHeaders(ctx, endpoint, nil, out)
}

func (p *Plugin) getJSONWithHeaders(ctx context.Context, endpoint string, headers map[string]string, out any) *pluginerrors.PluginError {
	if pluginErr := p.waitForRequestSlot(ctx); pluginErr != nil {
		return pluginErr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeUnknown, err.Error())
	}
	req.Header.Set("Accept", "application/vnd.api+json")
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeTimeout, err.Error())
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return pluginerrors.New(pluginerrors.ErrCodeUnauthorized, "kitsu authorization failed")
	case http.StatusNotFound:
		return pluginerrors.New(pluginerrors.ErrCodeNotFound, "kitsu resource not found")
	case http.StatusTooManyRequests:
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeRateLimited, "kitsu rate limited the request")
	default:
		if resp.StatusCode >= 500 {
			return pluginerrors.NewRetryable(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu returned status %d", resp.StatusCode))
		}
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu returned status %d", resp.StatusCode))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, err.Error())
	}
	return nil
}

func (p *Plugin) postJSON(ctx context.Context, endpoint string, headers map[string]string, body any, out any) *pluginerrors.PluginError {
	if pluginErr := p.waitForRequestSlot(ctx); pluginErr != nil {
		return pluginErr
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeUnknown, err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeUnknown, err.Error())
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeTimeout, err.Error())
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return pluginerrors.New(pluginerrors.ErrCodeUnauthorized, "kitsu algolia authorization failed")
	case http.StatusNotFound:
		return pluginerrors.New(pluginerrors.ErrCodeNotFound, "kitsu algolia index not found")
	case http.StatusTooManyRequests:
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeRateLimited, "kitsu algolia rate limited the request")
	default:
		if resp.StatusCode >= 500 {
			return pluginerrors.NewRetryable(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu algolia returned status %d", resp.StatusCode))
		}
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu algolia returned status %d", resp.StatusCode))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, err.Error())
	}
	return nil
}

func (p *Plugin) postForm(ctx context.Context, endpoint string, values url.Values, out any) *pluginerrors.PluginError {
	if pluginErr := p.waitForRequestSlot(ctx); pluginErr != nil {
		return pluginErr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeUnknown, err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeTimeout, err.Error())
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return pluginerrors.New(pluginerrors.ErrCodeUnauthorized, "kitsu token refresh failed")
	default:
		if resp.StatusCode >= 500 {
			return pluginerrors.NewRetryable(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu token endpoint returned status %d", resp.StatusCode))
		}
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, fmt.Sprintf("kitsu token endpoint returned status %d", resp.StatusCode))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, err.Error())
	}
	return nil
}

func (p *Plugin) waitForRequestSlot(ctx context.Context) *pluginerrors.PluginError {
	p.rateMu.Lock()
	now := time.Now()
	waitUntil := now
	if p.nextRequestAt.After(now) {
		waitUntil = p.nextRequestAt
	}
	p.nextRequestAt = waitUntil.Add(requestGap)
	p.rateMu.Unlock()

	delay := time.Until(waitUntil)
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return pluginerrors.NewRetryable(pluginerrors.ErrCodeTimeout, "kitsu request pacing was interrupted")
	case <-timer.C:
		return nil
	}
}

func (p *Plugin) searchWithAlgolia(ctx context.Context, req *sdktypes.SearchRequest, query string, limit int) ([]sdktypes.SearchCandidate, *pluginerrors.PluginError) {
	keyInfo, pluginErr := p.fetchAlgoliaMediaKey(ctx)
	if pluginErr != nil {
		return nil, pluginErr
	}

	var payload algoliaSearchResponse
	endpoint := strings.TrimRight(p.algoliaBaseURL, "/") + "/1/indexes/" + url.PathEscape(keyInfo.Index) + "/query"
	body := algoliaSearchRequest{
		Query:       query,
		Filters:     "kind:manga",
		HitsPerPage: limit,
	}
	headers := map[string]string{
		"Content-Type":             "application/json",
		"X-Algolia-Application-Id": algoliaAppID,
		"X-Algolia-API-Key":        keyInfo.Key,
	}
	if pluginErr := p.postJSON(ctx, endpoint, headers, body, &payload); pluginErr != nil {
		return nil, pluginErr
	}

	candidates := make([]sdktypes.SearchCandidate, 0, len(payload.Hits))
	for index, item := range payload.Hits {
		if !isAllowedKind(item.Kind) || !isAllowedSubtypeForContentType(req.ContentType, item.Subtype) {
			continue
		}
		candidates = append(candidates, mapAlgoliaCandidate(req, item, index))
	}
	return candidates, nil
}

func (p *Plugin) fetchAlgoliaMediaKey(ctx context.Context) (*algoliaIndexKey, *pluginerrors.PluginError) {
	var payload algoliaKeyResponse
	if p.accessToken == "" {
		return nil, pluginerrors.New(pluginerrors.ErrCodeUnauthorized, "kitsu access token is required for algolia search")
	}

	headers := map[string]string{
		"Authorization": "Bearer " + p.accessToken,
	}
	if pluginErr := p.getJSONWithHeaders(ctx, p.baseURL+"/algolia-keys", headers, &payload); pluginErr != nil {
		if pluginErr.Code == pluginerrors.ErrCodeUnauthorized && p.refreshToken != "" {
			if refreshErr := p.refreshAccessToken(ctx); refreshErr == nil {
				headers["Authorization"] = "Bearer " + p.accessToken
				if retryErr := p.getJSONWithHeaders(ctx, p.baseURL+"/algolia-keys", headers, &payload); retryErr == nil {
					goto validated
				}
			}
		}
		return nil, pluginErr
	}

validated:
	if strings.TrimSpace(payload.Media.Key) == "" || strings.TrimSpace(payload.Media.Index) == "" {
		return nil, pluginerrors.New(pluginerrors.ErrCodeProviderError, "kitsu algolia media key is missing")
	}
	return &payload.Media, nil
}

func (p *Plugin) refreshAccessToken(ctx context.Context) *pluginerrors.PluginError {
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", p.refreshToken)

	var payload oauthTokenResponse
	if pluginErr := p.postForm(ctx, "https://kitsu.io/api/oauth/token", values, &payload); pluginErr != nil {
		return pluginErr
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return pluginerrors.New(pluginerrors.ErrCodeProviderError, "kitsu token refresh returned an empty access token")
	}
	p.accessToken = strings.TrimSpace(payload.AccessToken)
	if refreshed := strings.TrimSpace(payload.RefreshToken); refreshed != "" {
		p.refreshToken = refreshed
	}
	return nil
}

func validateContentType(req *sdktypes.SearchRequest) *pluginerrors.PluginError {
	if req == nil {
		return nil
	}

	switch req.ContentType {
	case "", sdktypes.ContentTypeComic, sdktypes.ContentTypeNovel:
		return nil
	default:
		return pluginerrors.New(pluginerrors.ErrCodeUnsupported, fmt.Sprintf("content type %q is not supported by kitsu manga", req.ContentType))
	}
}

func isAllowedSubtypeForContentType(contentType sdktypes.ContentType, subtype string) bool {
	normalized := strings.ToLower(strings.TrimSpace(subtype))
	switch contentType {
	case sdktypes.ContentTypeNovel:
		return normalized == "novel"
	case "", sdktypes.ContentTypeComic:
		return normalized == "" || normalized == "manga" || normalized == "manhwa" || normalized == "manhua" || normalized == "oneshot" || normalized == "doujin" || normalized == "oel" || normalized == "webtoon"
	default:
		return false
	}
}

func contentTypeForSubtype(subtype string) sdktypes.ContentType {
	switch strings.ToLower(strings.TrimSpace(subtype)) {
	case "novel":
		return sdktypes.ContentTypeNovel
	case "", "manga", "manhwa", "manhua", "oneshot", "doujin", "oel", "webtoon":
		return sdktypes.ContentTypeComic
	default:
		return sdktypes.ContentTypeComic
	}
}

func isSupportedSubtype(subtype string) bool {
	switch strings.ToLower(strings.TrimSpace(subtype)) {
	case "", "manga", "manhwa", "manhua", "oneshot", "doujin", "oel", "webtoon", "novel":
		return true
	default:
		return false
	}
}

func isAllowedKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "manga":
		return true
	default:
		return false
	}
}

func buildSearchQuery(req *sdktypes.SearchRequest) string {
	if req == nil {
		return ""
	}
	for _, value := range []string{req.SeriesName, req.LocalTitle, req.Filename} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mapCandidate(req *sdktypes.SearchRequest, item mangaResource, index int) sdktypes.SearchCandidate {
	query := normalize(firstNonEmpty(req.SeriesName, req.LocalTitle, req.Filename))
	title := chooseSearchTitle(req, item.Attributes)
	variants := titleVariants(item.Attributes)
	confidence := candidateConfidence(query, variants)
	score := 1.0 - (float64(index) * 0.08)
	if score < 0.2 {
		score = 0.2
	}

	candidate := sdktypes.SearchCandidate{
		Source: sdktypes.SourceRef{
			ID:   item.ID,
			Name: providerName,
			URL:  resourceURL(item.Attributes.Slug, item.ID),
		},
		Title:         title,
		OriginalTitle: chooseOriginalTitle(item.Attributes),
		Description:   candidateDescription(item.Attributes),
		ContentType:   contentTypeForSubtype(item.Attributes.Subtype),
		CoverURL:      coverURL(item.Attributes.PosterImage),
		Score:         score,
		Confidence:    confidence,
		Reason:        candidateReason(query, variants, item.Attributes),
	}
	if year := yearFromDate(item.Attributes.StartDate); year > 0 {
		candidate.Year = &year
	}
	return candidate
}

func mapAlgoliaCandidate(req *sdktypes.SearchRequest, item algoliaMediaHit, index int) sdktypes.SearchCandidate {
	attrs := item.attributes()
	query := normalize(firstNonEmpty(req.SeriesName, req.LocalTitle, req.Filename))
	title := chooseSearchTitle(req, attrs)
	variants := titleVariants(attrs)
	confidence := candidateConfidence(query, variants)
	score := 1.0 - (float64(index) * 0.08)
	if score < 0.2 {
		score = 0.2
	}

	candidate := sdktypes.SearchCandidate{
		Source: sdktypes.SourceRef{
			ID:   strconv.Itoa(item.ID),
			Name: providerName,
			URL:  resourceURL(item.Slug, strconv.Itoa(item.ID)),
		},
		Title:         title,
		OriginalTitle: chooseOriginalTitle(attrs),
		Description:   candidateDescription(attrs),
		ContentType:   contentTypeForSubtype(item.Subtype),
		CoverURL:      coverURL(item.PosterImage),
		Score:         score,
		Confidence:    confidence,
		Reason:        candidateReason(query, variants, attrs),
	}
	if item.Year > 0 {
		year := item.Year
		candidate.Year = &year
	}
	return candidate
}

func (h algoliaMediaHit) attributes() mangaAttributes {
	attrs := mangaAttributes{
		Slug:              h.Slug,
		Synopsis:          strings.TrimSpace(h.Synopsis),
		Titles:            h.Titles,
		CanonicalTitle:    h.CanonicalTitle,
		AbbreviatedTitles: h.AbbreviatedTitles,
		AgeRating:         h.AgeRating,
		Subtype:           h.Subtype,
		PosterImage:       h.PosterImage,
		ChapterCount:      h.ChapterCount,
		VolumeCount:       h.VolumeCount,
	}
	if attrs.Synopsis == "" {
		attrs.Synopsis = strings.TrimSpace(firstMapValue(h.Description))
	}
	if h.StartDateUnix > 0 {
		attrs.StartDate = unixDate(h.StartDateUnix)
	}
	return attrs
}

func buildFetchTags(payload fetchResponse) []string {
	categories := make([]string, 0)
	for _, item := range payload.Included {
		if item.Type != "categories" {
			continue
		}
		label := strings.TrimSpace(firstNonEmpty(item.Attributes.Title, item.Attributes.Name))
		if label == "" {
			continue
		}
		categories = append(categories, label)
	}
	if len(categories) > 0 {
		return compactStrings(categories...)
	}
	return compactStrings(
		payload.Data.Attributes.Subtype,
		payload.Data.Attributes.Status,
		payload.Data.Attributes.AgeRating,
		payload.Data.Attributes.MangaType,
	)
}

func buildFetchCharacters(payload fetchResponse) []sdktypes.MetadataCharacter {
	characterByID := make(map[string]includedResource)
	var mediaCharacters []includedResource

	for _, item := range payload.Included {
		switch item.Type {
		case "characters":
			if strings.TrimSpace(item.ID) != "" {
				characterByID[item.ID] = item
			}
		case "mediaCharacters":
			mediaCharacters = append(mediaCharacters, item)
		}
	}

	characters := make([]sdktypes.MetadataCharacter, 0, len(mediaCharacters))
	for _, mediaChar := range mediaCharacters {
		role := strings.TrimSpace(mediaChar.Attributes.Role)
		characterID := strings.TrimSpace(mediaChar.Relationships.Character.Data.ID)
		if characterID == "" {
			continue
		}
		character, ok := characterByID[characterID]
		if !ok {
			continue
		}
		name := strings.TrimSpace(firstNonEmpty(character.Attributes.CanonicalName, character.Attributes.Name, character.Attributes.Title))
		if name == "" {
			continue
		}
		characters = append(characters, sdktypes.MetadataCharacter{
			ID:    characterID,
			Name:  name,
			Role:  role,
			Image: coverFrom(character.Attributes.Image),
			Identifiers: map[string]string{
				"kitsu_character_id":       characterID,
				"kitsu_media_character_id": strings.TrimSpace(mediaChar.ID),
			},
		})
	}

	sort.SliceStable(characters, func(i, j int) bool {
		return metadataCharacterLess(characters[i], characters[j])
	})
	if len(characters) > maxFetchedCharacters {
		characters = characters[:maxFetchedCharacters]
	}
	return characters
}

func metadataCharacterLess(left, right sdktypes.MetadataCharacter) bool {
	leftMain := isMainCharacterRole(left.Role)
	rightMain := isMainCharacterRole(right.Role)
	if leftMain != rightMain {
		return leftMain
	}

	leftName := strings.ToLower(strings.TrimSpace(left.Name))
	rightName := strings.ToLower(strings.TrimSpace(right.Name))
	if leftName != rightName {
		if leftName == "" {
			return false
		}
		if rightName == "" {
			return true
		}
		return leftName < rightName
	}

	return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
}

func isMainCharacterRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "main")
}

func (p *Plugin) mapEdgeCandidates(req *sdktypes.SearchRequest, items []mangaResource) []sdktypes.SearchCandidate {
	candidates := make([]sdktypes.SearchCandidate, 0, len(items))
	for index, item := range items {
		if !isAllowedSubtypeForContentType(req.ContentType, item.Attributes.Subtype) {
			continue
		}
		candidates = append(candidates, mapCandidate(req, item, index))
	}
	return candidates
}

func chooseSearchTitle(req *sdktypes.SearchRequest, attrs mangaAttributes) string {
	if value := strings.TrimSpace(attrs.CanonicalTitle); value != "" {
		return value
	}
	if req != nil {
		if value := titleByLanguage(attrs.Titles, string(req.Language)); value != "" {
			return value
		}
	}
	for _, lang := range []string{"ko", "en", "ja"} {
		if value := titleByLanguage(attrs.Titles, lang); value != "" {
			return value
		}
	}
	return strings.TrimSpace(firstNonEmpty(attrs.CanonicalTitle, firstTitle(attrs.Titles)))
}

func chooseFetchTitle(attrs mangaAttributes) string {
	for _, lang := range []string{"ko", "en", "ja"} {
		if value := titleByLanguage(attrs.Titles, lang); value != "" {
			return value
		}
	}
	return strings.TrimSpace(firstNonEmpty(attrs.CanonicalTitle, firstTitle(attrs.Titles)))
}

func chooseOriginalTitle(attrs mangaAttributes) string {
	for _, lang := range []string{"en", "ko", "ja"} {
		if value := titleByLanguage(attrs.Titles, lang); value != "" {
			return value
		}
	}
	return strings.TrimSpace(firstNonEmpty(attrs.CanonicalTitle, firstTitle(attrs.Titles)))
}

func originalTitles(titles map[string]string) map[string]string {
	if len(titles) == 0 {
		return nil
	}

	values := map[string]string{}
	for _, lang := range []string{"en", "ko", "ja"} {
		if value := titleByLanguage(titles, lang); value != "" {
			values[lang] = value
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func titleVariants(attrs mangaAttributes) []string {
	values := make([]string, 0, len(attrs.Titles)+len(attrs.AbbreviatedTitles)+1)
	values = append(values, attrs.CanonicalTitle)
	values = append(values, firstTitle(attrs.Titles))
	for _, value := range attrs.Titles {
		values = append(values, value)
	}
	values = append(values, attrs.AbbreviatedTitles...)
	return compactStrings(values...)
}

func titleByLanguage(titles map[string]string, lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return ""
	}
	for key, value := range titles {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(normalizedKey, lang+"_") || normalizedKey == lang {
			// Skip romaji variants for fetch title selection.
			if strings.Contains(normalizedKey, "romaji") {
				continue
			}
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func firstTitle(titles map[string]string) string {
	for _, key := range []string{"en", "en_us", "en_jp", "ko_kr", "ja_jp"} {
		if value := strings.TrimSpace(titles[key]); value != "" {
			return value
		}
	}
	for _, value := range titles {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func candidateConfidence(query string, variants []string) float64 {
	confidence := 0.35
	if query == "" {
		return confidence
	}
	for _, variant := range variants {
		current := normalize(variant)
		switch {
		case current == "":
		case current == query:
			return 0.96
		case strings.Contains(current, query) || strings.Contains(query, current):
			if confidence < 0.82 {
				confidence = 0.82
			}
		case tokenOverlap(query, current) >= 0.5:
			if confidence < 0.68 {
				confidence = 0.68
			}
		}
	}
	return confidence
}

func candidateReason(query string, variants []string, attrs mangaAttributes) string {
	if query != "" {
		for _, variant := range variants {
			current := normalize(variant)
			switch {
			case current == query:
				return "title exact match"
			case current != "" && (strings.Contains(current, query) || strings.Contains(query, current)):
				return "title partial match"
			}
		}
	}

	contextParts := compactStrings(attrs.Subtype, attrs.Status)
	if len(contextParts) > 0 {
		return strings.Join(contextParts, " · ")
	}
	return "provider ranking"
}

func candidateDescription(attrs mangaAttributes) string {
	text := strings.TrimSpace(firstNonEmpty(attrs.Description, attrs.Synopsis))
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 220 {
		return strings.TrimSpace(text[:217]) + "..."
	}
	return text
}

func mapIdentifiers(item mangaResource) map[string]string {
	identifiers := map[string]string{
		"kitsu_id": strings.TrimSpace(item.ID),
	}
	if slug := strings.TrimSpace(item.Attributes.Slug); slug != "" {
		identifiers["kitsu_slug"] = slug
	}
	return identifiers
}

func coverFrom(images imageSet) *sdktypes.CoverInfo {
	url := coverURL(images)
	if url == "" {
		return nil
	}
	return &sdktypes.CoverInfo{URL: url}
}

func coverURL(images imageSet) string {
	for _, value := range []string{images.Original, images.Large, images.Medium, images.Small, images.Tiny} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resourceURL(slug string, id string) string {
	slug = strings.TrimSpace(slug)
	if slug != "" {
		return "https://kitsu.app/manga/" + slug
	}
	return "https://kitsu.app/manga/" + url.PathEscape(strings.TrimSpace(id))
}

func limitOrDefault(limit int, fallback int, max int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}

func yearFromDate(value string) int {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil {
		return 0
	}
	return year
}

func languageFromTitles(titles map[string]string) string {
	for _, lang := range []string{"ko", "en", "ja"} {
		if value := titleByLanguage(titles, lang); value != "" {
			return lang
		}
	}
	return ""
}

func normalize(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r), strings.ContainsRune("-_:./", r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func unixDate(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format("2006-01-02")
}

func tokenOverlap(left string, right string) float64 {
	leftTokens := strings.Fields(left)
	rightTokens := strings.Fields(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}

	rightSet := make(map[string]struct{}, len(rightTokens))
	for _, token := range rightTokens {
		rightSet[token] = struct{}{}
	}
	matched := 0
	for _, token := range leftTokens {
		if _, ok := rightSet[token]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(leftTokens))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstMapValue(values map[string]string) string {
	for _, key := range []string{"en", "en_us", "en_jp", "ja_jp", "ko_kr"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func compactStrings(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	compacted := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		compacted = append(compacted, trimmed)
	}
	return compacted
}
