// Package slots manages the slot registry for the gharp portal.
//
// It reads slots.yaml (written by the operator-run provisioner) and upserts
// slot records into the portal store, and provides helpers for slot assignment.
//
// # Usage
//
// Load the registry at startup (and on admin "reload slots"):
//
//	f, _ := os.Open("provisioner/slots.yaml")
//	defer f.Close()
//	if err := slots.LoadRegistry(f, store); err != nil {
//	    log.Fatal(err)
//	}
//
// Assign a free slot to a user:
//
//	a := slots.NewAssigner(store)
//	asgn, err := a.AssignFreeSlot(userID)
//
// # Dependencies
//
// Requires gopkg.in/yaml.v3 (YAML parsing). Everything else is standard library.
// The Store interface is defined in this package; the concrete internal/store
// package satisfies it structurally and need not be imported here.
package slots
