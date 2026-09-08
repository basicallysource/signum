package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhoamiRejectsAnotherApplication(t *testing.T) {
	for _, audience := range []string{"", "https://tracker.example", "https://other.example"} {
		t.Run(audience, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"account":"reader","handle":"reader","token":{"audience":%q}}`, audience)
			}))
			defer server.Close()
			_, err := whoami(t.Context(), server.URL, "credential", "https://tracker.example")
			if (err != nil) != (audience == "https://other.example") {
				t.Fatalf("audience %q error %v", audience, err)
			}
		})
	}
}
