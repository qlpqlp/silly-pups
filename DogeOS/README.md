# DogeOS Portal (DogeBox PUP)

Live status dashboard for **DogeOS Chikyu testnet** on DogeBox.

It does **not** run a sequencer. It polls public endpoints and shows what is available today:

- Chain height, average block time, peers, client version
- Gas price, base fee, explorer slow/avg/fast bands
- Rough execution cost for a 21k transfer and 50k approve
- `L1GasPriceOracle` at `0x5300…0002` (overhead, scalar, L1 base fee, sample data/finality fee)
- Explorer totals and recent blocks/transactions
- Links to [bridge](https://portal.testnet.dogeos.com/bridge), [faucet](https://faucet.testnet.dogeos.com), [explorer](https://blockscout.testnet.dogeos.com), and [docs](https://docs.dogeos.com/en/developers)

## Local run

```bash
cd DogeOS/service
go run .
# open http://127.0.0.1:8091/
```

## Sources

- RPC: `https://rpc.testnet.dogeos.com/`
- Explorer API: `https://blockscout.testnet.dogeos.com/api/v2/...`
- Fee model: https://docs.dogeos.com/en/developers/transaction-fees-on-dogeos
- Logo: from https://www.dogeos.com/apple-icon.png
