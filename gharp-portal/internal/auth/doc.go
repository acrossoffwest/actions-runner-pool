// Package auth implements GitHub OAuth login, server-side sessions, CSRF
// protection, and RBAC middleware for the gharp portal.
//
// # Wiring (WS-E does this)
//
//	states := auth.NewOAuthStates()
//	gh     := auth.NewHTTPGitHubClient(cfg.OAuthClientID, cfg.OAuthClientSecret)
//
//	mux.Handle("GET /auth/start",    auth.StartHandler(cfg, states))
//	mux.Handle("GET /auth/callback", auth.CallbackHandler(cfg, gh, store, states))
//	mux.Handle("POST /auth/logout",  auth.LogoutHandler(store))
//
//	userGroup  := alice.New(auth.RequireUser(store))
//	adminGroup := alice.New(auth.RequireAdmin(store), auth.CSRFMiddleware)
//
// # Auth flow
//
//  1. GET /auth/start — CSPRNG state stored in OAuthStates, redirect to GitHub.
//  2. GET /auth/callback — validate state, exchange code, fetch GitHub user,
//     call UpsertUserOnLogin (gate). Bootstrap admin path calls InviteUser first.
//     On success: rotate session, set HttpOnly Secure SameSite=Lax cookie.
//  3. POST /auth/logout — delete session, clear cookie.
//
// # Context values
//
// RequireUser middleware stores auth.User and auth.Session in the context.
// Retrieve them with UserFromContext and SessionFromContext.
// CSRFMiddleware reads the session CSRF token from context.
// IssueCSRF(r) returns the token for HTML templates.
//
// # Dependencies
//
//   - internal/store (WS-A) via an adapter in WS-E — this package never imports it.
//   - Standard library only.
package auth
