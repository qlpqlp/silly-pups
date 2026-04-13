package mempooltracker

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	dogeMagicMainnet = 0xC0C0C0C0
	dogeMagicTestnet = 0xFCC1B7DC
	msgTx            = 1
	msgWitnessFlag   = 1 << 30
)

type Options struct {
	StorageDir string
	Network    string
}

type liveMempoolTx struct {
	Txid      string
	FirstSeen time.Time
	LastSeen  time.Time
}

type Engine struct {
	mu       sync.RWMutex
	stopCh   chan struct{}
	stopped  chan struct{}
	liveByID map[string]liveMempoolTx
}

func Start(opts Options) (*Engine, error) {
	if strings.TrimSpace(opts.StorageDir) == "" {
		return nil, errors.New("mempooltracker: StorageDir required")
	}
	e := &Engine{
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
		liveByID: map[string]liveMempoolTx{},
	}
	network := strings.ToLower(strings.TrimSpace(opts.Network))
	if network == "" {
		network = strings.ToLower(strings.TrimSpace(os.Getenv("NETWORK")))
	}
	if network == "" {
		network = "mainnet"
	}
	go e.loop(network)
	return e, nil
}

func (e *Engine) Stop() {
	close(e.stopCh)
	<-e.stopped
}

func (e *Engine) DashboardSnapshot() (mempoolCount int, live []map[string]any, workers []map[string]any, workersConnected int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := time.Now().UTC().Add(-3 * time.Minute)
	for txid, row := range e.liveByID {
		if row.LastSeen.Before(cutoff) {
			delete(e.liveByID, txid)
		}
	}
	rows := make([]liveMempoolTx, 0, len(e.liveByID))
	for _, row := range e.liveByID {
		rows = append(rows, row)
	}
	for i := 0; i < len(rows)-1; i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].LastSeen.After(rows[i].LastSeen) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if len(rows) > 250 {
		rows = rows[:250]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"txid":          row.Txid,
			"first_seen":    row.FirstSeen.Format(time.RFC3339),
			"last_seen":     row.LastSeen.Format(time.RFC3339),
			"tracked_match": false,
			"address":       "",
			"amount_doge":   0.0,
		})
	}
	return len(e.liveByID), out, nil, 0
}

func (e *Engine) markSeen(txid string) {
	if txid == "" {
		return
	}
	now := time.Now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	if row, ok := e.liveByID[txid]; ok {
		row.LastSeen = now
		e.liveByID[txid] = row
		return
	}
	e.liveByID[txid] = liveMempoolTx{Txid: txid, FirstSeen: now, LastSeen: now}
}

func (e *Engine) loop(network string) {
	defer close(e.stopped)
	seeds := []string{"seed.dogecoin.org", "seed.dogecoin.net", "seed.multidoge.org", "seed2.multidoge.org"}
	port := 22556
	magic := uint32(dogeMagicMainnet)
	if network != "mainnet" {
		seeds = []string{"testseed.jrn.me.uk"}
		port = 44556
		magic = uint32(dogeMagicTestnet)
	}
	for {
		select {
		case <-e.stopCh:
			return
		default:
		}
		for _, seed := range seeds {
			select {
			case <-e.stopCh:
				return
			default:
			}
			ips, err := net.LookupHost(seed)
			if err != nil || len(ips) == 0 {
				continue
			}
			for _, ip := range ips {
				select {
				case <-e.stopCh:
					return
				default:
				}
				addr := net.JoinHostPort(ip, strconv.Itoa(port))
				conn, err := net.DialTimeout("tcp", addr, 7*time.Second)
				if err != nil {
					continue
				}
				_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
				_ = writeMessage(conn, magic, "version", buildVersionPayload(port))
				_ = e.session(conn, magic)
				_ = conn.Close()
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func (e *Engine) session(conn net.Conn, magic uint32) error {
	gotVerack := false
	sentMempool := false
	for {
		select {
		case <-e.stopCh:
			return nil
		default:
		}
		cmd, payload, err := readMessage(conn, magic)
		if err != nil {
			return err
		}
		switch cmd {
		case "version":
			_ = writeMessage(conn, magic, "verack", nil)
		case "verack":
			gotVerack = true
			if !sentMempool {
				sentMempool = true
				_ = writeMessage(conn, magic, "mempool", nil)
			}
		case "ping":
			_ = writeMessage(conn, magic, "pong", payload)
		case "inv":
			if !gotVerack {
				continue
			}
			txids := parseInvTxids(payload)
			for _, txid := range txids {
				e.markSeen(txid)
			}
		case "tx":
			txid := txidHex(payload)
			e.markSeen(txid)
		}
	}
}

func parseInvTxids(payload []byte) []string {
	off := 0
	n, err := readVarInt(payload, &off)
	if err != nil {
		return nil
	}
	out := make([]string, 0, int(n))
	for i := 0; i < int(n); i++ {
		if off+36 > len(payload) {
			break
		}
		t := binary.LittleEndian.Uint32(payload[off:])
		h := payload[off+4 : off+36]
		off += 36
		if (t & ^uint32(msgWitnessFlag)) != msgTx {
			continue
		}
		rev := make([]byte, 32)
		for j := 0; j < 32; j++ {
			rev[j] = h[31-j]
		}
		out = append(out, hex.EncodeToString(rev))
	}
	return out
}

func readVarInt(data []byte, off *int) (uint64, error) {
	if *off >= len(data) {
		return 0, errors.New("eof")
	}
	b0 := data[*off]
	*off++
	if b0 < 0xfd {
		return uint64(b0), nil
	}
	if b0 == 0xfd {
		if *off+2 > len(data) {
			return 0, errors.New("eof")
		}
		v := binary.LittleEndian.Uint16(data[*off:])
		*off += 2
		return uint64(v), nil
	}
	if b0 == 0xfe {
		if *off+4 > len(data) {
			return 0, errors.New("eof")
		}
		v := binary.LittleEndian.Uint32(data[*off:])
		*off += 4
		return uint64(v), nil
	}
	if *off+8 > len(data) {
		return 0, errors.New("eof")
	}
	v := binary.LittleEndian.Uint64(data[*off:])
	*off += 8
	return v, nil
}

func buildVersionPayload(p2pPort int) []byte {
	out := make([]byte, 0, 110)
	put32 := func(v uint32) { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); out = append(out, b...) }
	put64 := func(v uint64) { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); out = append(out, b...) }
	put32(70015)
	put64(1 | 8)
	put64(uint64(time.Now().Unix()))
	addr := make([]byte, 26)
	binary.BigEndian.PutUint16(addr[24:], uint16(p2pPort))
	out = append(out, addr...)
	out = append(out, addr...)
	put64(uint64(time.Now().UnixNano()))
	ua := []byte("/QuantumExplorer:0.1.1/")
	out = append(out, byte(len(ua)))
	out = append(out, ua...)
	put32(0)
	out = append(out, 1)
	return out
}

func writeMessage(conn net.Conn, magic uint32, cmd string, payload []byte) error {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], magic)
	copy(hdr[4:16], []byte(cmd))
	binary.LittleEndian.PutUint32(hdr[16:], uint32(len(payload)))
	sum := sha256.Sum256(payload)
	sum2 := sha256.Sum256(sum[:])
	copy(hdr[20:24], sum2[:4])
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := conn.Write(payload)
		return err
	}
	return nil
}

func readMessage(conn net.Conn, magic uint32) (string, []byte, error) {
	hdr := make([]byte, 24)
	if _, err := ioReadFull(conn, hdr); err != nil {
		return "", nil, err
	}
	gotMagic := binary.LittleEndian.Uint32(hdr[0:4])
	if gotMagic != magic {
		return "", nil, errors.New("wrong magic")
	}
	cmd := strings.TrimRight(string(hdr[4:16]), "\x00")
	size := int(binary.LittleEndian.Uint32(hdr[16:20]))
	if size < 0 || size > 8*1024*1024 {
		return "", nil, errors.New("bad payload size")
	}
	payload := make([]byte, size)
	if size > 0 {
		if _, err := ioReadFull(conn, payload); err != nil {
			return "", nil, err
		}
	}
	return cmd, payload, nil
}

func ioReadFull(conn net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := conn.Read(buf[n:])
		if err != nil {
			return n, err
		}
		n += m
	}
	return n, nil
}

func txidHex(raw []byte) string {
	sum := sha256.Sum256(raw)
	sum2 := sha256.Sum256(sum[:])
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = sum2[31-i]
	}
	return hex.EncodeToString(rev)
}
