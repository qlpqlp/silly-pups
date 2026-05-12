package main

import "strings"

// pqAlgoKind selects which libdogecoin `such` PQC surface matches this wallet row.
type pqAlgoKind int

const (
	pqAlgoFalcon pqAlgoKind = iota
	pqAlgoDilithium2
	pqAlgoRaccoonG
)

// inferPQAlgo returns the PQ algorithm implied by wallet metadata (PQSource / PQScheme).
func inferPQAlgo(wf *WalletFile) pqAlgoKind {
	if wf == nil {
		return pqAlgoFalcon
	}
	src := strings.ToLower(strings.TrimSpace(wf.PQSource))
	switch {
	case strings.Contains(src, "dilithium"):
		return pqAlgoDilithium2
	case strings.Contains(src, "raccoon"):
		return pqAlgoRaccoonG
	case strings.Contains(src, "falcon"):
		return pqAlgoFalcon
	}
	sch := strings.ToLower(strings.TrimSpace(wf.PQScheme))
	switch {
	case strings.Contains(sch, "dilithium"):
		return pqAlgoDilithium2
	case strings.Contains(sch, "raccoon"):
		return pqAlgoRaccoonG
	}
	return pqAlgoFalcon
}

// pqCarrierTag4Hex is the 4-byte ASCII tag as 8 hex chars for pqc_carrier_mkpart -k (FLC1, DIL2, RCG4).
func pqCarrierTag4Hex(kind pqAlgoKind) string {
	switch kind {
	case pqAlgoDilithium2:
		return "44494c32"
	case pqAlgoRaccoonG:
		return "52434734"
	default:
		return "464c4331"
	}
}

func suchAddCommitCarrierCmd(kind pqAlgoKind) string {
	switch kind {
	case pqAlgoDilithium2:
		return "dilithium2_add_commit_and_carrier_tx"
	case pqAlgoRaccoonG:
		return "raccoong_add_commit_and_carrier_tx"
	default:
		return "falcon_add_commit_and_carrier_tx"
	}
}

func suchSignCmd(kind pqAlgoKind) string {
	switch kind {
	case pqAlgoDilithium2:
		return "dilithium2_sign"
	case pqAlgoRaccoonG:
		return "raccoong_sign"
	default:
		return "falcon_sign"
	}
}

// pqPhase1CanonicalMode labels how the Phase-1 commitment was derived (for logs / API).
func pqPhase1CanonicalMode(kind pqAlgoKind) string {
	switch kind {
	case pqAlgoDilithium2:
		return "phase1_canonical_dilithium2"
	case pqAlgoRaccoonG:
		return "phase1_canonical_raccoong"
	default:
		return "phase1_canonical_falcon"
	}
}
