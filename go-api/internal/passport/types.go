package passport

import (
	"context"
	"net/http"
)

type AuthData struct {
	Token    string `json:"token"`
	IsAdmin  int64  `json:"is_admin"`
	AuthData string `json:"auth_data"`
}

type SendEmailVerifyRequest struct {
	Email         string
	RecaptchaData string
	IsForget      int
	HasIsForget   bool
	IP            string
	UserAgent     string
}

type RegisterRequest struct {
	Email         string
	Password      string
	InviteCode    string
	EmailCode     string
	RecaptchaData string
	IP            string
	UserAgent     string
}

type LoginRequest struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
}

type ForgetRequest struct {
	Email     string
	Password  string
	EmailCode string
}

type TokenLoginRequest struct {
	Token     string
	Verify    string
	Redirect  string
	IP        string
	UserAgent string
}

type QuickLoginRequest struct {
	AuthData string
	Redirect string
}

type LoginWithMailLinkRequest struct {
	Email    string
	Redirect string
}

type TokenLoginResult struct {
	RedirectURL string
	AuthData    *AuthData
}

type Service interface {
	PV(ctx context.Context, inviteCode string) error
	SendEmailVerify(ctx context.Context, req SendEmailVerifyRequest) error
	Register(ctx context.Context, req RegisterRequest) (AuthData, error)
	Login(ctx context.Context, req LoginRequest) (AuthData, error)
	Forget(ctx context.Context, req ForgetRequest) error
	TokenLogin(ctx context.Context, req TokenLoginRequest) (TokenLoginResult, error)
	GetQuickLoginURL(ctx context.Context, req QuickLoginRequest) (string, error)
	LoginWithMailLink(ctx context.Context, req LoginWithMailLinkRequest) (any, error)
}

type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return e.Message
}

func NewHTTPError(status int, message string) error {
	return &HTTPError{Status: status, Message: message}
}

func IsHTTPStatus(err error, status int) bool {
	httpErr, ok := err.(*HTTPError)
	return ok && httpErr.Status == status
}

func HTTPStatus(err error) int {
	httpErr, ok := err.(*HTTPError)
	if !ok {
		return http.StatusInternalServerError
	}
	if httpErr.Status <= 0 {
		return http.StatusInternalServerError
	}
	return httpErr.Status
}
