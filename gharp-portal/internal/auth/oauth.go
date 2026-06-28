package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// StartHandler redirects the browser to GitHub's OAuth authorization endpoint
// with a CSPRNG state token to prevent CSRF on the callback.
func StartHandler(cfg Config, states *OAuthStates) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, err := randomToken(16)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		states.Put(state)

		u := &url.URL{
			Scheme: "https",
			Host:   "github.com",
			Path:   "/login/oauth/authorize",
		}
		q := u.Query()
		q.Set("client_id", cfg.OAuthClientID)
		q.Set("redirect_uri", cfg.BaseURL+"/auth/callback")
		q.Set("scope", "read:user")
		q.Set("state", state)
		u.RawQuery = q.Encode()

		http.Redirect(w, r, u.String(), http.StatusFound)
	})
}

// CallbackHandler handles the GitHub OAuth callback:
//  1. Validates the state param (prevents CSRF).
//  2. Exchanges the code for an access token via GitHubClient.
//  3. Fetches the GitHub user id+login.
//  4. Upserts the user via Store (gate: not-invited → 403).
//  5. Bootstrap admin promotion if login matches cfg.BootstrapAdminLogin.
//  6. Creates a session cookie (rotates any existing session).
func CallbackHandler(cfg Config, gh GitHubClient, st Store, states *OAuthStates) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		code := q.Get("code")

		if !states.Claim(state) {
			http.Error(w, "invalid or expired state", http.StatusBadRequest)
			return
		}

		accessToken, err := gh.ExchangeCode(r.Context(), code)
		if err != nil {
			http.Error(w, "code exchange failed", http.StatusBadRequest)
			return
		}

		ghUser, err := gh.GetUser(r.Context(), accessToken)
		if err != nil {
			http.Error(w, "failed to fetch GitHub user", http.StatusInternalServerError)
			return
		}

		user, err := st.UpsertUserOnLogin(ghUser.ID, ghUser.Login)
		if err != nil {
			if errors.Is(err, ErrNotInvited) {
				// Bootstrap admin bypass: create an admin invite on the fly.
				if cfg.BootstrapAdminLogin != "" &&
					strings.EqualFold(ghUser.Login, cfg.BootstrapAdminLogin) {
					if _, ie := st.InviteUser(ghUser.Login, "admin"); ie != nil {
						http.Error(w, "internal error", http.StatusInternalServerError)
						return
					}
					user, err = st.UpsertUserOnLogin(ghUser.ID, ghUser.Login)
					if err != nil {
						http.Error(w, "internal error", http.StatusInternalServerError)
						return
					}
				} else {
					http.Error(w, "403 not invited", http.StatusForbidden)
					return
				}
			} else {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}

		if user.Status == "disabled" {
			http.Error(w, "403 account disabled", http.StatusForbidden)
			return
		}

		// Rotate: delete any previous session.
		if c, cerr := r.Cookie(cookieName); cerr == nil {
			_ = st.DeleteSession(c.Value)
		}

		sess, err := st.CreateSession(user.ID, cfg.SessionTTL)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		_ = st.Audit(user.ID, "auth.login", ghUser.Login, "")

		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    sess.Token,
			Path:     "/",
			Expires:  sess.ExpiresAt,
			HttpOnly: true,
			Secure:   cfg.Secure,
			SameSite: http.SameSiteLaxMode,
		})

		if user.Role == "admin" {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/app", http.StatusSeeOther)
		}
	})
}

// LogoutHandler deletes the server-side session and clears the cookie.
// Must be mounted on POST /auth/logout.
func LogoutHandler(st Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if c, err := r.Cookie(cookieName); err == nil {
			if sess, found, _ := st.GetSession(c.Value); found {
				_ = st.Audit(sess.UserID, "auth.logout", "", "")
				_ = st.DeleteSession(c.Value)
			}
		}

		http.SetCookie(w, &http.Cookie{
			Name:   cookieName,
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})

		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}
