package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestAPIRateBuildups (EST-06, 0094): the rate library and build-up routes,
// including the ones the console does not drive (adding and correcting lines,
// percentages, revising), and their refusals.
func TestAPIRateBuildups(t *testing.T) {
	a := newP2PAPI(t)
	P := []string{a.project}
	est, com := a.tok("u-api-est", P, false, "estimator"), a.tok("u-api-com", P, false, "commercial_manager")
	eng := a.tok("u-api-eng", P, false, "site_engineer")
	outsider := a.tok("u-api-out", []string{uuid.NewString()}, false, "estimator")
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	rate := func(code, kind, unit, amount string) string {
		id := a.id("POST", "/v1/resource-rates", est, map[string]any{"legal_entity_id": a.entity, "code": code, "description": code + " supply", "kind": kind, "unit": unit,
			"currency": "ETB", "rate": amount, "source": "Price list 2026", "region": "Addis Ababa", "confidence": "MEDIUM", "valid_from": day(-5), "valid_to": day(300)})
		a.call(http.StatusOK, "POST", "/v1/resource-rates/"+id+"/approve", com, nil)
		return id
	}
	if st, _ := a.do("POST", "/v1/resource-rates", eng, map[string]any{"legal_entity_id": a.entity, "code": "X1", "description": "x", "kind": "MATERIAL", "unit": "t",
		"currency": "ETB", "rate": "1", "source": "none", "region": "x", "confidence": "LOW", "valid_from": day(0), "valid_to": day(1)}); st != http.StatusForbidden {
		t.Fatalf("a site engineer recorded a rate: %d", st)
	}
	mat, lab := rate("BLOCK-20", "MATERIAL", "pcs", "45"), rate("MASON", "LABOUR", "hr", "120")
	own := a.id("POST", "/v1/resource-rates", est, map[string]any{"legal_entity_id": a.entity, "code": "MORTAR", "description": "Mortar 1:4", "kind": "MATERIAL", "unit": "m3",
		"currency": "ETB", "rate": "3000", "source": "Price list 2026", "region": "Addis Ababa", "confidence": "LOW", "valid_from": day(-5), "valid_to": day(300)})
	if st, _ := a.do("POST", "/v1/resource-rates/"+own+"/approve", est, nil); st == http.StatusOK {
		t.Fatal("the recorder approved a rate over the API")
	}
	if st, _ := a.do("POST", "/v1/resource-rates/"+own+"/reject", com, map[string]any{}); st == http.StatusOK {
		t.Fatal("a rate was rejected without a reason")
	}
	a.call(http.StatusOK, "POST", "/v1/resource-rates/"+own+"/reject", com, map[string]any{"reason": "Source is a verbal quote"})

	// 1 m² of 200 mm blockwork: 12.5 blocks with 5% wastage, 1.2 hours of masonry.
	bu := a.id("POST", "/v1/rate-buildups", est, map[string]any{"project_id": a.project, "code": "BLK-200", "description": "200 mm blockwork", "unit": "m2", "currency": "ETB",
		"price_date": day(0), "overhead_pct": "8", "margin_pct": "10", "lines": []map[string]any{{"resource_rate_id": mat, "quantity": "12.5", "wastage_pct": "5"}}})
	if st, _ := a.do("POST", "/v1/rate-buildups/"+bu+"/lines", est, map[string]any{"lines": []map[string]any{{"resource_rate_id": own, "quantity": "0.02"}}}); st == http.StatusOK {
		t.Fatal("a rejected rate was added to a build-up")
	}
	a.call(http.StatusOK, "POST", "/v1/rate-buildups/"+bu+"/lines", est, map[string]any{"lines": []map[string]any{{"resource_rate_id": lab, "quantity": "1"}}})
	a.call(http.StatusOK, "POST", "/v1/rate-buildups/"+bu+"/lines/2", est, map[string]any{"quantity": "1.2", "wastage_pct": "0"})
	a.call(http.StatusOK, "POST", "/v1/rate-buildups/"+bu+"/percentages", est, map[string]any{"overhead_pct": "10", "contingency_pct": "0", "escalation_pct": "0", "margin_pct": "10"})
	// 12.5 × 1.05 × 45 = 590.625; 1.2 × 120 = 144; direct 734.625; full 808.0875; selling 808.0875 / 0.9 = 897.875 → 897.88.
	f := a.call(http.StatusOK, "GET", "/v1/rate-buildups/"+bu+"/figures", est, nil)
	if f["direct_cost"] != "734.625" || f["full_cost"] != "808.0875" || f["selling_rate"] != "897.88" || f["fixed"] != false {
		t.Fatalf("figures: %v", f)
	}
	if st, _ := a.do("GET", "/v1/rate-buildups/"+bu+"/figures", outsider, nil); st != http.StatusNotFound {
		t.Fatalf("another project's estimator read the figures: %d", st)
	}
	if st, _ := a.do("POST", "/v1/rate-buildups/"+bu+"/lines/9", est, map[string]any{"quantity": "1", "wastage_pct": "0"}); st == http.StatusOK {
		t.Fatal("a missing line was corrected")
	}
	a.call(http.StatusOK, "POST", "/v1/rate-buildups/"+bu+"/submit", est, nil)
	if st, _ := a.do("POST", "/v1/rate-buildups/"+bu+"/percentages", est, map[string]any{"overhead_pct": "0", "contingency_pct": "0", "escalation_pct": "0", "margin_pct": "0"}); st == http.StatusOK {
		t.Fatal("a submitted build-up's percentages changed")
	}
	if st, _ := a.do("POST", "/v1/rate-buildups/"+bu+"/approve", est, nil); st == http.StatusOK {
		t.Fatal("the author approved the build-up")
	}
	a.call(http.StatusOK, "POST", "/v1/rate-buildups/"+bu+"/approve", com, nil)
	if f := a.call(http.StatusOK, "GET", "/v1/rate-buildups/"+bu+"/figures", com, nil); f["selling_rate"] != "897.88" || f["fixed"] != true {
		t.Fatalf("approved figures: %v", f)
	}
	rev := a.call(http.StatusCreated, "POST", "/v1/rate-buildups/"+bu+"/revise", est, map[string]any{"price_date": day(10)})
	d := a.call(http.StatusOK, "GET", "/v1/p2p/rate_buildup/"+rev["id"].(string), est, nil)
	if d["number"] != "BLK-200/R2" || d["status"] != "DRAFT" {
		t.Fatalf("revision: %v", d)
	}
	if st, _ := a.do("POST", "/v1/rate-buildups/"+bu+"/revise", est, map[string]any{"price_date": day(10)}); st == http.StatusCreated {
		t.Fatal("a second open revision was started")
	}
}
