package backup

import (
	"encoding/json"
	"errors"
	"strings"
)

var ErrRunAdmissionTransportClosed = errors.New("backup: run admission transport unsupported")

func admissionZero(v any) bool {
	switch n := v.(type) {
	case int:
		return n == 0
	case int64:
		return n == 0
	case float64:
		return n == 0
	case json.Number:
		return n == "0"
	}
	return false
}
func admissionInputModern(d *DBDump) bool {
	if d == nil {
		return false
	}
	if _, ok := d.Tables["pipeline_run_admissions"]; ok {
		return true
	}
	for _, r := range d.Tables["pipeline_runs"] {
		for c := range r {
			if strings.HasPrefix(strings.ToLower(c), "admission_") {
				return true
			}
		}
	}
	return false
}

// Input is untrusted. This only refuses unsupported transport, never establishes source age or authority.
func rejectRunAdmissionInput(d *DBDump, requireMarkers bool) error {
	if d == nil {
		return nil
	}
	modern := requireMarkers || admissionInputModern(d)
	for table, rows := range d.Tables {
		if strings.HasPrefix(strings.ToLower(table), "pipeline_run_admission") && (table != "pipeline_run_admissions" || len(rows) != 0) {
			return ErrRunAdmissionTransportClosed
		}
	}
	for _, r := range d.Tables["pipeline_runs"] {
		for c := range r {
			if strings.HasPrefix(strings.ToLower(c), "admission_") && c != "admission_id" && c != "admission_required_format" {
				return ErrRunAdmissionTransportClosed
			}
		}
		f, fok := r["admission_required_format"]
		id, iok := r["admission_id"]
		if modern && (!fok || !iok) || fok && !admissionZero(f) || iok && id != nil {
			return ErrRunAdmissionTransportClosed
		}
	}
	return nil
}
