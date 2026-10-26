package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tirthpatel002/consona-package-manager/internal/core"
)

// resolutionSource is implemented by sources that record where each
// package came from (internal/source/chain).
type resolutionSource interface {
	ResolutionsSince(t time.Time) []string
}

// mountExtras adds optional endpoints the source supports.
func mountExtras(mux *http.ServeMux, src core.Source) {
	rs, ok := src.(resolutionSource)
	if !ok {
		return
	}
	mux.HandleFunc("/-/consona/resolutions", func(w http.ResponseWriter, r *http.Request) {
		ns, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		_ = json.NewEncoder(w).Encode(rs.ResolutionsSince(time.Unix(0, ns)))
	})
}

// ResolutionsSince fetches "name@version <- source" lines the daemon
// recorded after t.
func ResolutionsSince(c Config, t time.Time) []string {
	resp, err := client.Get(fmt.Sprintf("%s/-/consona/resolutions?since=%d", c.BaseURL(), t.UnixNano()))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out []string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}
