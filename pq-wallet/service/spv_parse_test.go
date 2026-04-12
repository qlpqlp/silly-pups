package main

import "testing"

func TestParseConnectedNodeLine(t *testing.T) {
	id, ua, h, ok := parseConnectedNodeLine("Connected to node 3: /DogecoinCore:1.14.6/ (5900000)")
	if !ok || id != 3 || h != 5900000 || ua != "/DogecoinCore:1.14.6/" {
		t.Fatalf("got id=%d ua=%q h=%d ok=%v", id, ua, h, ok)
	}
	// User agent may contain " (", last " (height)" wins
	id, ua, h, ok = parseConnectedNodeLine(`Connected to node 12: /Test:1.0 (note) more/ (700000)`)
	if !ok || id != 12 || h != 700000 {
		t.Fatalf("nested parens: id=%d ua=%q h=%d ok=%v", id, ua, h, ok)
	}
}

func TestParseCurrentPeerInfoPQOnly(t *testing.T) {
	log := "noise\nPQ_PEER node=2 ip=203.0.113.50 height=6000000\n"
	p := parseCurrentPeerInfo(log)
	if p == nil || p.NodeID != 2 || p.Address != "203.0.113.50" || p.RemoteStartHeight != 6000000 {
		t.Fatalf("got %+v", p)
	}
}
