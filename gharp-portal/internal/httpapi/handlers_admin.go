package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
)

const auditTailLimit = 50

type userRow struct {
	User       User
	Assignment *Assignment
}

type slotRow struct {
	Slot          Slot
	AssignedLogin string
}

type adminStats struct {
	UsersActive, UsersInvited, UsersDisabled    int
	SlotsAssigned, SlotsFree, SlotsDisabled     int
	InstancesRunning, InstancesError, Instances int
	AssignedPct                                 int // slots assigned / total, for the meter
}

type adminPageData struct {
	CurrentUser User
	CSRF        string
	Users       []userRow
	Slots       []slotRow
	Audit       []AuditEntry
	Stats       adminStats
	Flash       string
	Error       string
}

type auditPageData struct {
	CurrentUser User
	CSRF        string
	Audit       []AuditEntry
}

func handleAdminDashboard(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		csrf := CSRFFromContext(r.Context())

		data := adminPageData{CurrentUser: u, CSRF: csrf}

		users, _ := d.Store.ListUsers()
		slots, _ := d.Store.ListSlots()
		audit, _ := d.Store.ListAuditLog(auditTailLimit)

		// Build user rows with assignment info.
		for _, usr := range users {
			row := userRow{User: usr}
			a, ok, _ := d.Store.GetAssignmentByUser(usr.ID)
			if ok {
				ac := a
				row.Assignment = &ac
			}
			data.Users = append(data.Users, row)
		}

		// Build a map of slotID → assigned user login for slot rows.
		slotAssignee := map[string]string{}
		for _, row := range data.Users {
			if row.Assignment != nil {
				slotAssignee[row.Assignment.SlotID] = row.User.Login
			}
		}
		for _, sl := range slots {
			data.Slots = append(data.Slots, slotRow{
				Slot:          sl,
				AssignedLogin: slotAssignee[sl.ID],
			})
		}

		data.Audit = audit

		// Overview counts for the ribbon + stat tiles.
		for _, row := range data.Users {
			switch row.User.Status {
			case "active":
				data.Stats.UsersActive++
			case "invited":
				data.Stats.UsersInvited++
			case "disabled":
				data.Stats.UsersDisabled++
			}
			if row.Assignment != nil {
				switch row.Assignment.GharpState {
				case "running":
					data.Stats.InstancesRunning++
				case "error":
					data.Stats.InstancesError++
				}
			}
		}
		for _, row := range data.Slots {
			switch row.Slot.Status {
			case "assigned":
				data.Stats.SlotsAssigned++
			case "free":
				data.Stats.SlotsFree++
			case "disabled":
				data.Stats.SlotsDisabled++
			}
		}
		data.Stats.Instances = data.Stats.InstancesRunning + data.Stats.InstancesError
		if n := len(data.Slots); n > 0 {
			data.Stats.AssignedPct = data.Stats.SlotsAssigned * 100 / n
		}

		// Surface query-param flash/error (set after redirect).
		data.Flash = r.URL.Query().Get("flash")
		data.Error = r.URL.Query().Get("err")

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "admin", data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	}
}

func handleAdminInvite(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		login := r.FormValue("login")
		role := r.FormValue("role")
		if login == "" {
			redirect(w, r, "/admin", "err", "login is required")
			return
		}
		if role != "admin" && role != "user" {
			role = "user"
		}
		actor, _ := UserFromContext(r.Context())
		if _, err := d.Store.InviteUser(login, role); err != nil {
			redirect(w, r, "/admin", "err", "invite failed: "+err.Error())
			return
		}
		_ = d.Store.Audit(actor.ID, "user.invite", login, fmt.Sprintf("role=%s", role))
		redirect(w, r, "/admin", "flash", "Invited "+login)
	}
}

func handleAdminUserStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}
		status := r.FormValue("status")
		if status != "active" && status != "disabled" {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		actor, _ := UserFromContext(r.Context())
		if err := d.Store.SetUserStatus(id, status); err != nil {
			redirect(w, r, "/admin", "err", "status update failed: "+err.Error())
			return
		}
		// Disabling an account must also stop its gharp instance — otherwise the
		// runner keeps processing work even though the user is locked out.
		// Best-effort: a stop failure (e.g. no assignment) must not block the
		// status change, which already succeeded.
		if status == "disabled" {
			if err := d.Lifecycle.Stop(id); err != nil {
				_ = d.Store.Audit(actor.ID, "gharp.stop.failed", idStr, err.Error())
			}
		}
		_ = d.Store.Audit(actor.ID, "user.status", idStr, fmt.Sprintf("status=%s", status))
		redirect(w, r, "/admin", "flash", "Status updated")
	}
}

func handleAdminAssignSlot(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		idStr := r.PathValue("id")
		userID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}
		actor, _ := UserFromContext(r.Context())
		slotID := r.FormValue("slot_id")

		var a Assignment
		if slotID != "" {
			a, err = d.Store.AssignSlot(userID, slotID)
		} else {
			a, err = d.Store.AssignFreeSlot(userID)
		}
		if err != nil {
			redirect(w, r, "/admin", "err", "assign failed: "+err.Error())
			return
		}
		_ = d.Store.Audit(actor.ID, "slot.assign", idStr, fmt.Sprintf("slot=%s", a.SlotID))
		redirect(w, r, "/admin", "flash", "Assigned slot "+a.SlotID)
	}
}

func handleAdminSlotsReload(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := UserFromContext(r.Context())
		if err := d.SlotsReloader.Reload(); err != nil {
			redirect(w, r, "/admin", "err", "reload failed: "+err.Error())
			return
		}
		_ = d.Store.Audit(actor.ID, "slots.reload", "", "")
		redirect(w, r, "/admin", "flash", "Slots reloaded")
	}
}

func handleAdminAudit(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		csrf := CSRFFromContext(r.Context())

		entries, _ := d.Store.ListAuditLog(500)
		data := auditPageData{CurrentUser: u, CSRF: csrf, Audit: entries}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "audit", data); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
		}
	}
}

// redirect is a POST-Redirect-GET helper that passes a single flash or error
// message as a query parameter to avoid re-submitting on refresh.
func redirect(w http.ResponseWriter, r *http.Request, dest, key, val string) {
	loc := dest
	if key != "" && val != "" {
		loc += "?" + key + "=" + encodeParam(val)
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

func encodeParam(s string) string {
	// simple percent-encode for URL query param value (only what's needed)
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == ' ':
			if c == ' ' {
				out = append(out, '+')
			} else {
				out = append(out, c)
			}
		default:
			out = append(out, fmt.Sprintf("%%%02X", c)...)
		}
	}
	return string(out)
}
