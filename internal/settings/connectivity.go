package settings

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kksty/saker/internal/aop"
	"github.com/kksty/saker/internal/modelclient"
)

// ModelProbe is the settings page's connectivity answer. Probed=false means no
// request was made (nothing to test). ReturnedModel is the provider's own field
// from the inference response; an empty value is left empty rather than filled
// with the configured name.
type ModelProbe struct {
	Probed        bool     `json:"probed"`
	Available     bool     `json:"available"`
	Error         string   `json:"error,omitempty"`
	ReturnedModel string   `json:"returned_model,omitempty"`
	MS            int64    `json:"ms"`
	Models        []string `json:"models,omitempty"`
	// Source says which path answered: "host" (the running harness's own
	// ConfigService/TestLLM) or "self" (this process's direct probe).
	Source string `json:"source,omitempty"`
}

// CheckModel is the direct in-process probe: one lightweight inference with the
// effective connection. It is the fallback when the local host is not online.
func (s *Store) CheckModel(ctx context.Context) ModelProbe {
	m := s.Effective()
	c, err := modelclient.NewWithProvider(m.BaseURL, m.APIKey, m.Model, m.Provider)
	if err != nil {
		return ModelProbe{Error: err.Error(), Source: "self"}
	}
	c.MaxTokens = 256
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	started := time.Now()
	result, err := c.Check(ctx)
	out := ModelProbe{Probed: true, MS: time.Since(started).Milliseconds(), Source: "self"}
	if err != nil {
		out.Error = s.redactSecrets(err.Error())
	} else {
		out.Available = true
		out.ReturnedModel = result.ReturnedModel
	}
	return out
}

// connectProbe is the candidate connection the host should test: the saved
// effective values, explicitly supplied so the probe never depends on the host
// having already reloaded a just-saved model.
func (s *Store) connectProbe() aop.LLMProbe {
	m := s.Effective()
	return aop.LLMProbe{
		Provider: m.Provider,
		BaseURL:  m.BaseURL,
		APIKey:   m.APIKey,
		Model:    m.Model,
	}
}

// CheckModelOnline asks the running harness to probe the candidate connection
// itself (ConfigService/TestLLM) and, when it supports a catalogue endpoint,
// list the models (ListModels). The host is the component that will actually use
// the connection, so its answer is the real one: the effective runtime, proxy
// and provider normalisation are the host's, not this process's copy.
//
// Only a transport/RPC failure is returned as an error (the caller falls back to
// CheckModel). ok=false from the host is a valid probe answer — a wrong key must
// not silently fall back to a second request that would hide it.
func (s *Store) CheckModelOnline(ctx context.Context, baseURL, token string) (ModelProbe, error) {
	client := aop.New(baseURL, token)
	probe := s.connectProbe()
	started := time.Now()
	result, err := client.ProbeLLM(ctx, probe)
	if err != nil {
		// The host answers a failed probe (a bad key, a missing model) with
		// invalid_argument and its own message. That is an answer, not a
		// transport failure: returning it keeps the caller from probing twice
		// and masks nothing.
		var connectErr *aop.ConnectError
		if errors.As(err, &connectErr) && strings.EqualFold(connectErr.Code, "invalid_argument") {
			return ModelProbe{
				Probed: true, Available: false, Source: "host",
				Error: s.redactSecrets(connectErr.Message),
				MS:    time.Since(started).Milliseconds(),
			}, nil
		}
		return ModelProbe{}, err
	}
	out := ModelProbe{
		Probed: true, Available: result.OK, Source: "host",
		ReturnedModel: result.Model, MS: time.Since(started).Milliseconds(),
	}
	if result.LatencyMS > 0 {
		out.MS = result.LatencyMS
	}
	if result.Error != "" {
		out.Error = s.redactSecrets(result.Error)
	}
	// A catalogue is metadata, not a second inference: a provider without one
	// (Supported=false) or a failing call does not invalidate the probe.
	if models, modelErr := client.ListModels(ctx, probe); modelErr == nil && models.Supported {
		out.Models = models.Models
	}
	return out, nil
}

// ModelsOnline reads the endpoint catalogue through the running host. It returns
// ok=false when the host has no catalogue support, so the caller can keep its
// direct probe instead of showing an empty list as a success.
func (s *Store) ModelsOnline(ctx context.Context, baseURL, token string) (models []string, ok bool, err error) {
	result, err := aop.New(baseURL, token).ListModels(ctx, s.connectProbe())
	if err != nil {
		return nil, false, err
	}
	if !result.Supported {
		return nil, false, nil
	}
	return result.Models, true, nil
}

// redactSecrets keeps stored key material out of an error string the console
// displays.
func (s *Store) redactSecrets(text string) string {
	if text == "" {
		return ""
	}
	for _, secret := range s.SecretValues() {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}
