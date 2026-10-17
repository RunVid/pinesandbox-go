package tokens

import "context"

// CallableTokenSource adapts a caller's control-token provider (a backend that mints the
// project token itself, so this process never holds the pk_) to the source the control
// plane drives. An empty token is an error, never an unauthenticated request.
type CallableTokenSource struct {
	fn func(ctx context.Context, forceRefresh bool) (string, error)
}

// NewCallableTokenSource wraps fn.
func NewCallableTokenSource(fn func(ctx context.Context, forceRefresh bool) (string, error)) *CallableTokenSource {
	return &CallableTokenSource{fn: fn}
}

// Token returns the provider's project token.
func (s *CallableTokenSource) Token(ctx context.Context, forceRefresh bool) (string, error) {
	tok, err := s.fn(ctx, forceRefresh)
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", &ControlTokenError{tokenBase{Msg: "the control token provider returned an empty token"}}
	}
	return tok, nil
}

func (s *CallableTokenSource) String() string { return "tokens.CallableTokenSource{provider redacted}" }

// CallableAttachSource adapts a caller's attach-credentials issuer to the minter the bind
// handshake drives. Like the Portal-backed source it returns the receipt as issued; the
// binder records the revision and then rejects a non-advancing or incomplete receipt.
type CallableAttachSource struct {
	fn func(ctx context.Context, req CredentialsRequest) (*AttachCredentials, error)
}

// NewCallableAttachSource wraps fn.
func NewCallableAttachSource(fn func(ctx context.Context, req CredentialsRequest) (*AttachCredentials, error)) *CallableAttachSource {
	return &CallableAttachSource{fn: fn}
}

// Credentials calls the provider.
func (s *CallableAttachSource) Credentials(ctx context.Context, req CredentialsRequest) (*AttachCredentials, error) {
	creds, err := s.fn(ctx, req)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, &AttachCredentialsError{tokenBase{Msg: "the attach credentials provider returned no credentials"}}
	}
	return creds, nil
}

func (s *CallableAttachSource) String() string {
	return "tokens.CallableAttachSource{provider redacted}"
}
