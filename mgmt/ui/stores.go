package ui

import (
	"net/http"
	"strconv"
)

// Degraded platform stores (#147): a store whose object stopped its
// replay at a journal entry it can't apply (GET /v1/stores/degraded). The
// overview lists them, operators only, when there are any.

type degradedStore struct {
	TenantID string `json:"tenantID"`
	Workload string `json:"workload"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Degraded struct {
		Seq      int64  `json:"seq"`
		Error    string `json:"error"`
		Host     string `json:"host"`
		At       int64  `json:"at"`
		Workload string `json:"workload"`
	} `json:"degraded"`
}

// Fix is the CLI way out for the store.
func (s degradedStore) Fix() string {
	if s.Kind == "d1" {
		return "tw d1 export " + s.Name + " --workload " + s.Workload + " --output backup.sql, then tw d1 repair " + s.Name + " --skip " + strconv.FormatInt(s.Degraded.Seq, 10) + " --workload " + s.Workload
	}
	return "upgrade tw-hostd if a newer tailworks wrote the entry (docs/errors.md#store_degraded)"
}

func (u *ui) degradedStores(r *http.Request) ([]degradedStore, error) {
	var out struct {
		Items []degradedStore `json:"items"`
	}
	err := u.call(r, "GET", "/v1/stores/degraded", &out)
	return out.Items, err
}
