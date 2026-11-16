package httpapi

// Supplier qualification, conflict declarations and performance scores (SUP-03.3; 0093).
//
//	POST /v1/supplier-qualifications {supplier_id, category, reference, valid_from, valid_to}
//	POST /v1/supplier-qualifications/{id}/decide {approve, note}
//	GET  /v1/suppliers/{id}/standing
//	GET  /v1/rfqs/{id}/declarations; POST /v1/rfqs/{id}/declarations {supplier_id?, relationship?}
//	POST /v1/purchase-orders/{id}/score {quality, delivery, safety, note}

import (
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
)

func (s *Server) registerSupplierGovernance() {
	m, k := s.mux, s.cfg.Kernel
	m.Handle("POST /v1/supplier-qualifications", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SupplierQualificationInput
		if !decode(w, r, &b) || !validIDs(w, b.SupplierID) {
			return
		}
		id, err := k.RecordSupplierQualification(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/supplier-qualifications/{id}/decide", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Approve bool   `json:"approve"`
			Note    string `json:"note"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if err := k.DecideSupplierQualification(r.Context(), principalFrom(r.Context()), id, b.Approve, b.Note); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}))
	m.Handle("GET /v1/suppliers/{id}/standing", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.SupplierStandingOf(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("GET /v1/rfqs/{id}/declarations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.RFQDeclarations(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out})
	}))
	m.Handle("POST /v1/rfqs/{id}/declarations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			SupplierID   string `json:"supplier_id"`
			Relationship string `json:"relationship"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) || (b.SupplierID != "" && !validIDs(w, b.SupplierID)) {
			return
		}
		if err := k.DeclareRFQConflict(r.Context(), principalFrom(r.Context()), id, b.SupplierID, b.Relationship); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"rfq_id": id})
	}))
	m.Handle("POST /v1/purchase-orders/{id}/score", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Quality  int    `json:"quality"`
			Delivery int    `json:"delivery"`
			Safety   int    `json:"safety"`
			Note     string `json:"note"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if err := k.ScoreSupplier(r.Context(), principalFrom(r.Context()), id, b.Quality, b.Delivery, b.Safety, b.Note); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"po_id": id})
	}))
}
