package v8

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthConfig_Validate(t *testing.T) {
	tests := []struct {
		name        string
		auth        *AuthConfig
		provider    string
		errContains string
	}{
		{
			name: "nil auth is valid",
			auth: nil,
		},
		{
			name:        "missing type",
			auth:        &AuthConfig{},
			errContains: "auth.type is required",
		},
		{
			name:        "unknown type",
			auth:        &AuthConfig{Type: "oauth"},
			errContains: "unsupported auth.type",
		},
		{
			name: "wif on non-anthropic provider",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
				},
			},
			provider:    "openai",
			errContains: "only supported with the anthropic provider",
		},
		{
			name:        "wif requires federation block",
			auth:        &AuthConfig{Type: AuthTypeWorkloadIdentityFederation},
			provider:    "anthropic",
			errContains: "workload_identity_federation block is required",
		},
		{
			name:     "wif federation_rule_id required",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					OrganizationID: "org",
					IdentityToken:  &IdentityTokenSourceConfig{File: "/t"},
				},
			},
			errContains: "federation_rule_id is required",
		},
		{
			name:     "wif federation_rule_id prefix",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "bogus",
					OrganizationID:   "org",
					IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
				},
			},
			errContains: `must start with "fdrl_"`,
		},
		{
			name:     "wif organization_id required",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
				},
			},
			errContains: "organization_id is required",
		},
		{
			name:     "wif service_account_id prefix when set",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					ServiceAccountID: "bogus",
					IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
				},
			},
			errContains: `must start with "svac_"`,
		},
		{
			name:     "identity_token required",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
				},
			},
			errContains: "identity_token is required",
		},
		{
			name:     "identity_token requires exactly one source",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken:    &IdentityTokenSourceConfig{},
				},
			},
			errContains: "requires exactly one of",
		},
		{
			name:     "identity_token rejects multiple sources",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken:    &IdentityTokenSourceConfig{File: "/t", Env: "X"},
				},
			},
			errContains: "must set exactly one",
		},
		{
			name:     "headers without url is rejected",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken: &IdentityTokenSourceConfig{
						File:    "/t",
						Headers: map[string]string{"X": "Y"},
					},
				},
			},
			errContains: "headers can only be used with",
		},
		{
			name:     "response_field without url is rejected",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken: &IdentityTokenSourceConfig{
						Env:           "X",
						ResponseField: "value",
					},
				},
			},
			errContains: "response_field can only be used with",
		},
		{
			name:     "command rejects empty arg",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_x",
					OrganizationID:   "org",
					IdentityToken: &IdentityTokenSourceConfig{
						Command: []string{"sh", ""},
					},
				},
			},
			errContains: "command[1] must not be empty",
		},
		{
			name:     "valid file source",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_abc",
					OrganizationID:   "org",
					IdentityToken: &IdentityTokenSourceConfig{
						File: "/var/run/secrets/anthropic/token",
					},
				},
			},
		},
		{
			name:     "valid url source with headers",
			provider: "anthropic",
			auth: &AuthConfig{
				Type: AuthTypeWorkloadIdentityFederation,
				Federation: &FederationAuthConfig{
					FederationRuleID: "fdrl_abc",
					OrganizationID:   "org",
					ServiceAccountID: "svac_abc",
					IdentityToken: &IdentityTokenSourceConfig{
						URL:           "https://example.com/token",
						Headers:       map[string]string{"Authorization": "bearer ${TOKEN}"},
						ResponseField: "value",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.auth.validate(tt.provider)
			if tt.errContains == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestConfigValidate_AuthErrorsAreScoped verifies that auth validation errors
// from providers and models are surfaced with a scoping prefix that points the
// user at the offending block.
func TestConfigValidate_AuthErrorsAreScoped(t *testing.T) {
	t.Run("provider auth", func(t *testing.T) {
		cfg := Config{
			Providers: map[string]ProviderConfig{
				"anthropic-wif": {
					Provider: "anthropic",
					Auth:     &AuthConfig{Type: "oauth"},
				},
			},
		}
		err := cfg.validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "providers.anthropic-wif")
	})

	t.Run("model auth", func(t *testing.T) {
		cfg := Config{
			Models: map[string]ModelConfig{
				"claude": {
					Provider: "anthropic",
					Auth: &AuthConfig{
						Type: AuthTypeWorkloadIdentityFederation,
						Federation: &FederationAuthConfig{
							OrganizationID: "org",
							IdentityToken:  &IdentityTokenSourceConfig{File: "/t"},
						},
					},
				},
			},
		}
		err := cfg.validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "models.claude")
		assert.Contains(t, err.Error(), "federation_rule_id is required")
	})

	t.Run("model auth on a model that references a custom provider", func(t *testing.T) {
		// The model points at a custom-provider key ("my-anthropic") whose
		// underlying type is "anthropic". Validation must look through that
		// indirection rather than comparing the auth.type against the raw
		// provider key on the model.
		cfg := Config{
			Providers: map[string]ProviderConfig{
				"my-anthropic": {Provider: "anthropic"},
			},
			Models: map[string]ModelConfig{
				"claude": {
					Provider: "my-anthropic",
					Auth: &AuthConfig{
						Type: AuthTypeWorkloadIdentityFederation,
						Federation: &FederationAuthConfig{
							FederationRuleID: "fdrl_x",
							OrganizationID:   "org",
							IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
						},
					},
				},
			},
		}
		err := cfg.validate()
		assert.NoError(t, err)
	})

	t.Run("model auth rejected when referenced provider is not anthropic", func(t *testing.T) {
		cfg := Config{
			Providers: map[string]ProviderConfig{
				"my-openai": {Provider: "openai"},
			},
			Models: map[string]ModelConfig{
				"gpt": {
					Provider: "my-openai",
					Auth: &AuthConfig{
						Type: AuthTypeWorkloadIdentityFederation,
						Federation: &FederationAuthConfig{
							FederationRuleID: "fdrl_x",
							OrganizationID:   "org",
							IdentityToken:    &IdentityTokenSourceConfig{File: "/t"},
						},
					},
				},
			},
		}
		err := cfg.validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only supported with the anthropic provider")
	})
}
