package chatagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Provider is an OpenAI-compatible chat completions service. To add one, add
// an entry to providers; the agent's tools and limits are shared.
type Provider struct {
	// Label names the service in errors.
	Label string
	// BaseURL is the API root, without /chat/completions. It is fixed here and
	// never read from configuration, so nothing can redirect the API key.
	BaseURL string
	// KeyEnv is the environment variable that holds the API key, or "" for a
	// service that takes none.
	KeyEnv string
	// Request holds fields sent with every chat request.
	Request map[string]any
	// CheckModel rejects a model id the reviewer must not run on.
	CheckModel func(model string) error
	// ServerToolUse names a tool the service ran by itself for this response,
	// or returns "". Nil for a service that has none.
	ServerToolUse func(response []byte) string
}

var providers = map[string]Provider{
	"openrouter": {
		Label:   "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
		KeyEnv:  "OPENROUTER_API_KEY",
		Request: map[string]any{
			// Only endpoints that do not retain or train on prompts, and that
			// support the tools this agent sends.
			"provider": map[string]any{"data_collection": "deny", "require_parameters": true},
			// An account can turn the web plugin on for every request.
			"plugins": []any{map[string]any{"id": "web", "enabled": false}},
		},
		CheckModel:    checkOpenRouterModel,
		ServerToolUse: openRouterServerToolUse,
	},
}

// Lookup returns a provider by its adapter name.
func Lookup(name string) (Provider, bool) {
	p, ok := providers[name]
	return p, ok
}

// openRouterModel matches vendor/model with the routing variants that change
// neither what the request can reach nor which model answers it.
var openRouterModel = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*(:(free|nitro|floor|exacto))*$`)

// checkOpenRouterModel refuses ids that are not one named model: routers,
// presets and ~latest aliases choose or change the model themselves, so a
// saved review could not say what read the code. :online adds web search, and
// :batch answers too slowly for a check.
func checkOpenRouterModel(model string) error {
	if strings.HasPrefix(model, "openrouter/") {
		return fmt.Errorf("OpenRouter model %q is a router, which picks the model itself; name one model, such as vendor/model", model)
	}
	if !openRouterModel.MatchString(model) {
		return fmt.Errorf("invalid OpenRouter model %q: want vendor/model, optionally with :free, :nitro, :floor or :exacto (:online, :batch, ~latest aliases and presets are not allowed)", model)
	}
	return nil
}

// openRouterServerToolUse detects web search that ran despite the request's
// plugin setting, which an account's "prevent overrides" option can force.
func openRouterServerToolUse(response []byte) string {
	var r struct {
		Choices []struct {
			Message struct {
				Annotations []struct {
					Type string `json:"type"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			ServerTools map[string]json.Number `json:"server_tool_use_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(response, &r); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return ""
		}
		// A shape this check cannot read is treated as a tool having run.
		return "a server tool report SlopCop could not read"
	}
	for _, c := range r.Choices {
		for _, a := range c.Message.Annotations {
			if a.Type == "url_citation" {
				return "OpenRouter web search"
			}
		}
	}
	for name, count := range r.Usage.ServerTools {
		if n, err := count.Float64(); err != nil || n != 0 {
			return "OpenRouter server tool " + name
		}
	}
	return ""
}
