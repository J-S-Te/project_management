package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

const defaultEndpoint = "https://api.typesafe.ai/v1/systemone"

type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

func NewClient(endpoint, apiKey string, timeout time.Duration) *Client {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint == "" {
		endpoint = defaultEndpoint
	}
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 8 * time.Second
	}
	return &Client{endpoint: endpoint, apiKey: apiKey, http: &http.Client{Timeout: timeout}}
}

type noulQuestion struct {
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

type request struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]noulQuestion `json:"questions"`
}

type response struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type string  `json:"type"`
		Noul float64 `json:"noul"`
	} `json:"answers"`
}

func (c *Client) TriageDeviation(ctx context.Context, input domain.DeviationInput) (domain.DeviationTriageResult, error) {
	if c == nil {
		return domain.DeviationTriageResult{}, errors.New("typesafe client is disabled")
	}
	payload := request{
		State: map[string]any{
			"description":       strings.TrimSpace(input.Description),
			"reported_severity": strings.ToUpper(strings.TrimSpace(input.Severity)),
		},
		Model: "jev-latest",
		Questions: map[string]noulQuestion{
			"safety_risk":          {Type: "noul", Instructions: "Does the deviation describe a credible risk to a person's safety, system safety, or safe operation?", Criteria: map[string]any{"true": "A stated condition can cause injury, unsafe operation, or loss of a safety control", "false": "No concrete safety consequence is described"}},
			"compliance_risk":      {Type: "noul", Instructions: "Does the deviation credibly indicate a compliance, regulatory, qualification, authorization, or audit-evidence breach?", Criteria: map[string]any{"true": "A mandatory rule, approval, qualification, authorization, or evidence requirement may be violated", "false": "The deviation is operational only and does not indicate such a breach"}},
			"delivery_blocked":     {Type: "noul", Instructions: "Does the deviation prevent the service item from safely continuing or being delivered without human intervention?", Criteria: map[string]any{"true": "Work cannot responsibly proceed or deliver until someone resolves the issue", "false": "Work can continue under the documented process"}},
			"severity_understated": {Type: "noul", Instructions: "Is the reported severity likely lower than the consequences described in the deviation?", Criteria: map[string]any{"true": "The narrative supports a materially more severe classification than `reported_severity`", "false": "The reported severity is at least as high as the described consequences"}},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return domain.DeviationTriageResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.DeviationTriageResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return domain.DeviationTriageResult{}, fmt.Errorf("typesafe request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return domain.DeviationTriageResult{}, fmt.Errorf("typesafe request returned status %d", res.StatusCode)
	}
	var decoded response
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&decoded); err != nil {
		return domain.DeviationTriageResult{}, fmt.Errorf("decode typesafe response: %w", err)
	}
	wanted := []string{"safety_risk", "compliance_risk", "delivery_blocked", "severity_understated"}
	probabilities := make(map[string]float64, len(wanted))
	for _, key := range wanted {
		answer, ok := decoded.Answers[key]
		if !ok || answer.Type != "noul" || answer.Noul < 0 || answer.Noul > 1 {
			return domain.DeviationTriageResult{}, fmt.Errorf("typesafe response missing valid %s answer", key)
		}
		probabilities[key] = answer.Noul
	}
	return domain.DeviationTriageResult{Mode: "ADVISORY_ONLY", Model: decoded.Model, Probabilities: probabilities}, nil
}
