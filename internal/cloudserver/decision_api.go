package cloudserver

import (
	"net/http"

	"github.com/0xmarkhydra/codelocal/internal/webutil"
)

func (s *Server) decisionStatusAPI(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.identity(r)
	if !ok {
		webutil.JSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if s.MCP == nil {
		webutil.JSON(w, http.StatusOK, map[string]any{
			"mode":         "off",
			"provider":     "unavailable",
			"primaryReady": false,
			"managed":      true,
			"features":     map[string]bool{},
		})
		return
	}
	webutil.JSON(w, http.StatusOK, s.MCP.DecisionStatus(r.Context(), identity.User.ID))
}
