package httpapi

import (
	"net/http"
)

type userPageData struct {
	CurrentUser User
	CSRF        string
	Assignment  *Assignment
	Flash       string
	Error       string
}

func handleAppShell(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		csrf := CSRFFromContext(r.Context())

		data := userPageData{
			CurrentUser: u,
			CSRF:        csrf,
		}

		a, ok, err := d.Store.GetAssignmentByUser(u.ID)
		if err == nil && ok {
			ac := a // copy
			data.Assignment = &ac
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "user", data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	}
}

func handleAppStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		if err := d.Lifecycle.Start(u.ID); err != nil {
			http.Error(w, "start failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = d.Store.Audit(u.ID, "gharp.start", "", "")
		http.Redirect(w, r, "/app", http.StatusFound)
	}
}

func handleAppStop(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		if err := d.Lifecycle.Stop(u.ID); err != nil {
			http.Error(w, "stop failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = d.Store.Audit(u.ID, "gharp.stop", "", "")
		http.Redirect(w, r, "/app", http.StatusFound)
	}
}
