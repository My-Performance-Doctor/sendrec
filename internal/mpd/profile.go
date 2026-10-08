package mpd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var cognitoIssuerHost = regexp.MustCompile(`^cognito-idp\.([a-z]{2}(?:-[a-z]+)+-[0-9]+)\.amazonaws\.com$`)
var cognitoPoolName = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+_[A-Za-z0-9]+$`)

// GetUser supports Cognito InitiateAuth tokens with the user.admin scope but no
// OIDC openid scope. The destination comes only from the configured pool issuer.
func cognitoEndpoint(issuer string) (string, error) {
	u, e := url.Parse(issuer)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrDenied
	}
	match := cognitoIssuerHost.FindStringSubmatch(u.Host)
	pool := strings.TrimPrefix(u.Path, "/")
	if len(match) != 2 || !cognitoPoolName.MatchString(pool) || !strings.HasPrefix(pool, match[1]+"_") || u.RawPath != "" {
		return "", ErrDenied
	}
	return "https://" + u.Host + "/", nil
}
func (h *Handler) cognitoProfile(ctx context.Context, raw string, id *Identity) error {
	if id.Issuer != h.cfg.Issuer {
		return ErrDenied
	}
	endpoint, e := cognitoEndpoint(h.cfg.Issuer)
	if e != nil {
		return e
	}
	body, _ := json.Marshal(struct{ AccessToken string }{raw})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if e != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.GetUser")
	// Do not forward the token body to a redirect destination.
	client := *h.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		return ErrDenied
	}
	payload, e := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if e != nil {
		return ErrUnavailable
	}
	if len(payload) > 32768 {
		return ErrDenied
	}
	var result struct {
		UserAttributes []struct{ Name, Value string }
	}
	if json.Unmarshal(payload, &result) != nil {
		return ErrDenied
	}
	attributes := make(map[string]string)
	for _, a := range result.UserAttributes {
		if _, exists := attributes[a.Name]; exists {
			return ErrDenied
		}
		attributes[a.Name] = a.Value
	}
	if attributes["sub"] != id.Subject || attributes["email_verified"] != "true" || attributes["email"] == "" || len(attributes["email"]) > 320 {
		return ErrDenied
	}
	id.Email = attributes["email"]
	id.Name = attributes["name"]
	return nil
}
