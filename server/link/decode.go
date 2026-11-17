// SPDX-License-Identifier: AGPL-3.0-only

package link

import (
	"encoding/json"
	"net/http"
)

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
