package ditto

import (
	"context"
	"testing"
)

// NewCopy creates an ephemeral database copy on the ditto host, registers
// t.Cleanup to delete it when the test finishes, and returns the connection
// string. Calls t.Fatal on any error.
//
// Example:
//
//	dsn := ditto.NewCopy(t,
//	    ditto.WithServerURL("http://ditto.internal:8080"),
//	    ditto.WithToken(os.Getenv("DITTO_TOKEN")),
//	    ditto.WithTTL(10*time.Minute),
//	)
func NewCopy(t testing.TB, opts ...Option) string {
	t.Helper()

	c := New(opts...)
	cr, err := c.create(context.Background())
	if err != nil {
		t.Fatalf("ditto.NewCopy: %v", err)
	}

	t.Cleanup(func() {
		if err := c.destroy(context.Background(), cr.ID); err != nil {
			t.Logf("ditto.NewCopy cleanup: %v", err)
		}
	})

	return cr.ConnectionString
}
