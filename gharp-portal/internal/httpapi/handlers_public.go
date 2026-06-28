package httpapi

import (
	"net/http"
)

// handleRoot redirects authenticated users by role; unauthenticated visitors
// see the login page.
func handleRoot(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		u, ok := UserFromContext(r.Context())
		if !ok {
			// try to resolve from session cookie without RequireUser middleware
			u, ok = resolveUser(r, d)
		}
		if ok && u.Status == "active" {
			// Default landing is the user's runner (/app). An admin with no slot
			// assigned has nothing to do there, so send them to the console.
			_, hasSlot, _ := d.Store.GetAssignmentByUser(u.ID)
			if !hasSlot && u.Role == "admin" {
				http.Redirect(w, r, "/admin", http.StatusFound)
				return
			}
			http.Redirect(w, r, "/app", http.StatusFound)
			return
		}
		renderLogin(w, loginData{})
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	renderLogin(w, loginData{})
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func handleLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(d.cookieName())
		if err == nil && c.Value != "" {
			_ = d.Sessions.DeleteSession(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     d.cookieName(),
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// resolveUser attempts to identify the current user from the session cookie
// without relying on the RequireUser middleware being in the chain. Used for
// the root handler which sits outside the protected subtrees.
func resolveUser(r *http.Request, d Deps) (User, bool) {
	c, err := r.Cookie(d.cookieName())
	if err != nil || c.Value == "" {
		return User{}, false
	}
	sess, ok, err := d.Sessions.GetSession(c.Value)
	if err != nil || !ok {
		return User{}, false
	}
	u, ok, err := d.Store.GetUserByID(sess.UserID)
	if err != nil || !ok {
		return User{}, false
	}
	return u, true
}

type loginData struct {
	Error string
	Flash string
}

func renderLogin(w http.ResponseWriter, data loginData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "login", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
