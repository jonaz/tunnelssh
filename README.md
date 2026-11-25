# tunnelssh
reverse tunnels ssh over websocket

## Master metrics

`tunnelssh master --metrics-addr :9182` serves Prometheus metrics at
`/metrics` on that address, apart from `--port` so that whatever exposes
the master's port does not expose them too. Without the flag none are served.

- `tunnelssh_agent_sessions`: agents connected now.
- `tunnelssh_agent_connected{id}`: 1 for each connected agent id.
- `tunnelssh_agent_connects_total`, `tunnelssh_agent_disconnects_total`:
  sessions registered and ended, to catch an agent that flaps.
- `tunnelssh_agent_rejected_total{reason}`: refused connections;
  `duplicate_id` (the id already has a session), `invalid_id` or
  `handshake` (the websocket upgrade or the yamux session failed).

Agents connect without authentication (whoever uses a tunnel still has to
log in to the ssh behind it), so the master only takes ids of 1 to 64
characters from `A-Z a-z 0-9 . _ -` and answers 400 to any other. Each
connected id is a series of `tunnelssh_agent_connected`, so anyone who can
reach the master can add series: limit what a scrape may return
(Prometheus' `sample_limit`), and keep `/metrics` off the public network,
since it lists every agent id and has no authentication either.
